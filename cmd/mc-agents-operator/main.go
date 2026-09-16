// Command mc-agents-operator runs the MinecraftBot, MinecraftBotPool and MCPServer
// reconcilers against an in-cluster (or kubeconfig-supplied) Kubernetes API.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/mc-agents/operator/api/v1alpha1"
	"github.com/mc-agents/operator/internal/botimage"
	botctl "github.com/mc-agents/operator/internal/controller/bot"
	mcpserverctl "github.com/mc-agents/operator/internal/controller/mcpserver"
	poolctl "github.com/mc-agents/operator/internal/controller/pool"
	"github.com/mc-agents/operator/internal/mcpserverspec"
	"github.com/mc-agents/operator/internal/metrics"
	"github.com/mc-agents/operator/internal/podspec"
	"github.com/mc-agents/operator/internal/throttle"
)

const componentName = "mc-agents-operator"

var version = "dev"

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(v1alpha1.AddToScheme(scheme))
}

func main() {
	klog.InitFlags(nil)
	// --zap-log-level and friends. The production preset drops every V(1)+ line, and -v alone
	// only raises klog, so without these the operator's own debug lines could never be seen.
	zapOpts := zap.Options{}
	zapOpts.BindFlags(flag.CommandLine)

	opts := defaultOptions()
	opts.bind(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))
	// client-go and the leader election log through klog; one sink, one format.
	klog.SetLogger(ctrl.Log)

	if err := run(ctrl.SetupSignalHandler(), opts); err != nil {
		ctrl.Log.Error(err, "operator exited")
		os.Exit(1)
	}
}

func run(ctx context.Context, opts options) error {
	if err := opts.validate(); err != nil {
		return err
	}

	// --kubeconfig is registered by controller-runtime itself; GetConfig reads
	// it, then KUBECONFIG, then the in-cluster config, then ~/.kube/config.
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("load kubeconfig: %w", err)
	}

	mgr, err := ctrl.NewManager(cfg, managerOptions(opts))
	if err != nil {
		return fmt.Errorf("new manager: %w", err)
	}

	if err := mgr.AddHealthzCheck("ping", healthz.Ping); err != nil {
		return fmt.Errorf("add healthz: %w", err)
	}
	synced := &cacheSyncGate{}
	if err := mgr.Add(synced); err != nil {
		return fmt.Errorf("add cache sync gate: %w", err)
	}
	if err := mgr.AddReadyzCheck("cache-sync", synced.Check); err != nil {
		return fmt.Errorf("add readyz: %w", err)
	}

	// The events.v1 Recorder API would mean standing up our own broadcaster;
	// manager.GetEventRecorderFor still does it for us.
	recorder := mgr.GetEventRecorderFor(componentName) //nolint:staticcheck // SA1019
	c := mgr.GetClient()

	images := botimage.NewResolver(opts.botRegistry, botimage.Tags{
		Fabric: opts.fabricTag,
		Azalea: opts.azaleaTag,
	})
	builder := podspec.NewBuilder(images, podspec.Defaults{AssetFetcherImage: opts.assetFetcherImage})
	gate := throttle.NewIntervalGate(opts.spawnInterval)

	bots := botctl.NewReconciler(c, mgr.GetAPIReader(), recorder, builder, gate)
	if err := bots.SetupWithManager(mgr, opts.workers); err != nil {
		return fmt.Errorf("minecraftbot reconciler: %w", err)
	}
	if err := poolctl.NewReconciler(c, recorder).SetupWithManager(mgr, opts.workers); err != nil {
		return fmt.Errorf("minecraftbotpool reconciler: %w", err)
	}
	servers := mcpserverctl.NewReconciler(c, mgr.GetAPIReader(), recorder, mcpserverspec.Defaults{
		Repository: opts.mcpServerImage,
		Tag:        opts.mcpServerTag,
	})
	if err := servers.SetupWithManager(mgr, opts.workers); err != nil {
		return fmt.Errorf("mcpserver reconciler: %w", err)
	}
	if err := metrics.Register(mgr.GetCache()); err != nil {
		return fmt.Errorf("register metrics: %w", err)
	}

	ctrl.Log.Info("mc-agents-operator starting",
		"version", version,
		"namespaces", namespaceLabel(opts.watchNamespaces),
		"spawnInterval", opts.spawnInterval,
	)
	return mgr.Start(ctx)
}

func managerOptions(opts options) manager.Options {
	// One knob for the lease; the renew and retry windows have to stay inside
	// it or the manager refuses to start.
	renewDeadline := opts.leaderElectLeaseDuration * 2 / 3
	retryPeriod := opts.leaderElectLeaseDuration / 5

	// Everything the operator creates carries the managed-by label, so the cache never holds a pod,
	// Deployment or Role it did not create.
	managed := cache.ByObject{Label: labels.SelectorFromSet(labels.Set{
		v1alpha1.LabelManagedBy: v1alpha1.ManagedByValue,
	})}

	out := manager.Options{
		Scheme:                        scheme,
		Metrics:                       metricsserver.Options{BindAddress: opts.metricsAddress},
		HealthProbeBindAddress:        opts.probeAddress,
		LeaderElection:                opts.leaderElect,
		LeaderElectionID:              opts.leaderElectID,
		LeaderElectionNamespace:       opts.leaderElectNamespace,
		LeaderElectionReleaseOnCancel: true,
		LeaseDuration:                 &opts.leaderElectLeaseDuration,
		RenewDeadline:                 &renewDeadline,
		RetryPeriod:                   &retryPeriod,
		Cache: cache.Options{
			SyncPeriod: &opts.resyncPeriod,
			ByObject: map[client.Object]cache.ByObject{
				&corev1.Pod{}:                 managed,
				&appsv1.Deployment{}:          managed,
				&corev1.Service{}:             managed,
				&corev1.ServiceAccount{}:      managed,
				&networkingv1.NetworkPolicy{}: managed,
				&rbacv1.Role{}:                managed,
				&rbacv1.RoleBinding{}:         managed,
			},
		},
	}
	if len(opts.watchNamespaces) > 0 {
		out.Cache.DefaultNamespaces = map[string]cache.Config{}
		for _, ns := range opts.watchNamespaces {
			out.Cache.DefaultNamespaces[ns] = cache.Config{}
		}
	}
	return out
}

func namespaceLabel(ns namespaces) string {
	if len(ns) == 0 {
		return "all"
	}
	return ns.String()
}
