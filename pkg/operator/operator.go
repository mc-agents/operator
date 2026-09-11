package operator

import (
	"context"
	"fmt"
	"os"
	"sync"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"

	"github.com/mc-agents/operator/api/v1alpha1"
	"github.com/mc-agents/operator/pkg/apiclient"
	"github.com/mc-agents/operator/pkg/botimage"
	botctl "github.com/mc-agents/operator/pkg/controller/bot"
	poolctl "github.com/mc-agents/operator/pkg/controller/pool"
	"github.com/mc-agents/operator/pkg/podspec"
	"github.com/mc-agents/operator/pkg/queue"
	"github.com/mc-agents/operator/pkg/serve"
	"github.com/mc-agents/operator/pkg/throttle"
)

const componentName = "mc-agents-operator"

type Runner interface {
	Run(ctx context.Context) error
}

type operator struct {
	opts      Options
	config    *rest.Config
	scheme    *runtime.Scheme
	readiness *serve.Readiness
}

func New(opts Options) (Runner, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	config, err := restConfig(opts.Kubeconfig)
	if err != nil {
		return nil, err
	}

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(v1alpha1.AddToScheme(scheme))

	return &operator{
		opts:      opts,
		config:    config,
		scheme:    scheme,
		readiness: serve.NewReadiness(),
	}, nil
}

func (o *operator) Run(ctx context.Context) error {
	logger := klog.FromContext(ctx)

	clientset, err := kubernetes.NewForConfig(o.config)
	if err != nil {
		return fmt.Errorf("build kubernetes client: %w", err)
	}
	crds, err := apiclient.New(o.config)
	if err != nil {
		return err
	}

	servers := serve.New(o.opts.MetricsAddress, o.opts.ProbeAddress, o.readiness)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := servers.Run(ctx); err != nil {
			logger.Error(err, "http servers exited")
		}
	}()
	defer wg.Wait()

	lead := func(ctx context.Context) error {
		return o.runControllers(ctx, clientset, crds)
	}
	if !o.opts.LeaderElect {
		return lead(ctx)
	}
	return o.runElected(ctx, clientset, lead)
}

func (o *operator) runControllers(ctx context.Context, clientset kubernetes.Interface, crds apiclient.Interface) error {
	logger := klog.FromContext(ctx)

	recorder := o.newRecorder(ctx, clientset)

	podFactory := informers.NewSharedInformerFactoryWithOptions(clientset, o.opts.ResyncPeriod,
		informers.WithNamespace(o.opts.Namespace),
		informers.WithTweakListOptions(func(opts *metav1.ListOptions) {
			opts.LabelSelector = labels.SelectorFromSet(labels.Set{
				v1alpha1.LabelManagedBy: v1alpha1.ManagedByValue,
			}).String()
		}),
	)
	podInformer := podFactory.Core().V1().Pods()

	botInformer := crds.Bots().NewInformer(o.opts.Namespace, o.opts.ResyncPeriod)
	poolInformer := crds.Pools().NewInformer(o.opts.Namespace, o.opts.ResyncPeriod)

	botLister := apiclient.NewLister[*v1alpha1.MinecraftBot](botInformer.GetIndexer(), v1alpha1.Resource("minecraftbots"))
	poolLister := apiclient.NewLister[*v1alpha1.MinecraftBotPool](poolInformer.GetIndexer(), v1alpha1.Resource("minecraftbotpools"))

	images := botimage.NewResolver(o.opts.BotRegistry, botimage.Tags{
		Mineflayer: o.opts.MineflayerTag,
		Fabric:     o.opts.FabricTag,
	})
	builder := podspec.NewBuilder(images, podspec.Defaults{AssetFetcherImage: o.opts.AssetFetcherImage})

	botController := queue.NewController("minecraftbot", botctl.NewReconciler(
		crds.Bots(), botLister, clientset, podInformer.Lister(), builder,
		throttle.NewIntervalGate(o.opts.SpawnInterval), recorder,
	))
	poolController := queue.NewController("minecraftbotpool", poolctl.NewReconciler(
		crds.Pools(), poolLister, crds.Bots(), botLister, recorder,
	))

	if _, err := botInformer.AddEventHandler(queue.EnqueueSelf(botController)); err != nil {
		return fmt.Errorf("watch minecraftbots: %w", err)
	}
	if _, err := botInformer.AddEventHandler(queue.EnqueueController(poolController, "MinecraftBotPool")); err != nil {
		return fmt.Errorf("watch minecraftbots for pools: %w", err)
	}
	if _, err := poolInformer.AddEventHandler(queue.EnqueueSelf(poolController)); err != nil {
		return fmt.Errorf("watch minecraftbotpools: %w", err)
	}
	if _, err := podInformer.Informer().AddEventHandler(queue.EnqueueController(botController, "MinecraftBot")); err != nil {
		return fmt.Errorf("watch pods: %w", err)
	}

	botController.WaitFor(botInformer.HasSynced, podInformer.Informer().HasSynced)
	poolController.WaitFor(poolInformer.HasSynced, botInformer.HasSynced)

	go botInformer.Run(ctx.Done())
	go poolInformer.Run(ctx.Done())
	podFactory.Start(ctx.Done())

	if !cache.WaitForCacheSync(ctx.Done(),
		botInformer.HasSynced, poolInformer.HasSynced, podInformer.Informer().HasSynced) {
		return fmt.Errorf("cache sync failed")
	}
	o.readiness.SetReady(true)
	defer o.readiness.SetReady(false)
	logger.Info("watching", "namespace", namespaceLabel(o.opts.Namespace), "spawnInterval", o.opts.SpawnInterval)

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, controller := range []*queue.Controller{botController, poolController} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := controller.Run(ctx, o.opts.Workers); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	return <-errs
}

func (o *operator) runElected(ctx context.Context, clientset kubernetes.Interface, lead func(context.Context) error) error {
	namespace := o.opts.LeaderElectNamespace
	if namespace == "" {
		namespace = podNamespace()
	}
	if namespace == "" {
		return fmt.Errorf("leader election needs a namespace: set --leader-elect-namespace or POD_NAMESPACE")
	}
	identity, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("hostname for leader identity: %w", err)
	}

	lock := &resourcelock.LeaseLock{
		LeaseMeta: metav1.ObjectMeta{Name: o.opts.LeaderElectID, Namespace: namespace},
		Client:    clientset.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{
			Identity:      identity,
			EventRecorder: o.newRecorder(ctx, clientset),
		},
	}

	lease := o.opts.LeaderElectLeaseDuration
	runErr := make(chan error, 1)
	leaderelection.RunOrDie(ctx, leaderelection.LeaderElectionConfig{
		Lock:            lock,
		ReleaseOnCancel: true,
		LeaseDuration:   lease,
		RenewDeadline:   lease * 2 / 3,
		RetryPeriod:     lease / 5,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(ctx context.Context) {
				runErr <- lead(ctx)
			},
			OnStoppedLeading: func() {
				klog.FromContext(ctx).Info("lost leadership")
			},
		},
	})
	select {
	case err := <-runErr:
		return err
	default:
		return nil
	}
}

func (o *operator) newRecorder(ctx context.Context, clientset kubernetes.Interface) record.EventRecorder {
	broadcaster := record.NewBroadcaster()
	broadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: clientset.CoreV1().Events("")})
	go func() {
		<-ctx.Done()
		broadcaster.Shutdown()
	}()
	return broadcaster.NewRecorder(o.scheme, corev1.EventSource{Component: componentName})
}

func restConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig != "" {
		cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("load kubeconfig %s: %w", kubeconfig, err)
		}
		return cfg, nil
	}
	cfg, err := rest.InClusterConfig()
	if err == nil {
		return cfg, nil
	}
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	cfg, fallbackErr := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).ClientConfig()
	if fallbackErr != nil {
		return nil, fmt.Errorf("no in-cluster config (%v) and no usable kubeconfig: %w", err, fallbackErr)
	}
	return cfg, nil
}

func podNamespace() string {
	if ns := os.Getenv("POD_NAMESPACE"); ns != "" {
		return ns
	}
	data, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		return ""
	}
	return string(data)
}

func namespaceLabel(ns string) string {
	if ns == "" {
		return "all"
	}
	return ns
}
