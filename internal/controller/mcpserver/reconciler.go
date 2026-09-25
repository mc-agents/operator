// Package mcpserver reconciles an MCPServer into the Deployment, Services, NetworkPolicy,
// ServiceAccount, RBAC and tokens that run one MCP server in the tenant's own namespace.
package mcpserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"reflect"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/mc-agents/operator/api/v1alpha1"
	"github.com/mc-agents/operator/internal/mcpserverspec"
	"github.com/mc-agents/operator/internal/podstatus"
)

const (
	FieldOwner = "mc-agents-operator"

	reasonTokenCreated = "TokenCreated"
	reasonApplyFailed  = "ApplyFailed"
)

type Reconciler struct {
	client   client.Client
	recorder record.EventRecorder

	// apiReader reads Secrets without caching every Secret in every watched namespace.
	apiReader client.Reader

	defaults mcpserverspec.Defaults
	token    func() (string, error)
}

func NewReconciler(c client.Client, apiReader client.Reader, recorder record.EventRecorder, defaults mcpserverspec.Defaults) *Reconciler {
	return &Reconciler{
		client:    c,
		apiReader: apiReader,
		recorder:  recorder,
		defaults:  defaults,
		token:     newToken,
	}
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager, workers int) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("mcpserver").
		For(&v1alpha1.MCPServer{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&networkingv1.NetworkPolicy{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		// The server pod is the ReplicaSet's, not ours, so Owns does not reach it; what the kubelet
		// is stuck on has to come through its labels.
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(serverOf)).
		WithOptions(controller.Options{MaxConcurrentReconciles: workers}).
		Complete(r)
}

func serverOf(_ context.Context, obj client.Object) []reconcile.Request {
	labels := obj.GetLabels()
	if labels[v1alpha1.LabelName] != mcpserverspec.ComponentName || labels[v1alpha1.LabelMCPServer] == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: client.ObjectKey{Namespace: obj.GetNamespace(), Name: labels[v1alpha1.LabelMCPServer]}}}
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var cached v1alpha1.MCPServer
	if err := r.client.Get(ctx, req.NamespacedName, &cached); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	server := cached.DeepCopy()
	if !server.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	if server.Spec.Auth.ExistingSecret == "" {
		if err := r.ensureSecret(ctx, server, mcpserverspec.GeneratedSecretName(server), mcpserverspec.GeneratedSecret); err != nil {
			return ctrl.Result{}, r.fail(ctx, server, "TokenFailed", err)
		}
	}
	if err := r.ensureSecret(ctx, server, mcpserverspec.LinkSecretName(server), mcpserverspec.LinkSecret); err != nil {
		return ctrl.Result{}, r.fail(ctx, server, "TokenFailed", err)
	}

	objects := []client.Object{
		mcpserverspec.ServiceAccount(server),
		mcpserverspec.Service(server),
		mcpserverspec.BotService(server),
		mcpserverspec.NetworkPolicy(server),
	}
	if mcpserverspec.Provisions(server) {
		objects = append(objects, mcpserverspec.Role(server), mcpserverspec.RoleBinding(server))
	} else if err := r.removeRBAC(ctx, server); err != nil {
		return ctrl.Result{}, r.fail(ctx, server, reasonApplyFailed, err)
	}
	deployment := mcpserverspec.Deployment(server, r.defaults)
	objects = append(objects, deployment)

	for _, obj := range objects {
		if err := r.apply(ctx, obj); err != nil {
			r.recorder.Eventf(server, corev1.EventTypeWarning, reasonApplyFailed, "%v", err)
			return ctrl.Result{}, r.fail(ctx, server, reasonApplyFailed, err)
		}
	}

	// nil when the Get finds nothing, which the apply moments ago makes a cache that has not caught
	// up rather than a missing Deployment. Handing observe the zero value instead would pass an
	// empty DeploymentStatus off as the live one's, which reads the same as a pod not up yet.
	var live *appsv1.Deployment
	var found appsv1.Deployment
	switch err := r.client.Get(ctx, client.ObjectKeyFromObject(deployment), &found); {
	case err == nil:
		live = &found
	case !apierrors.IsNotFound(err):
		return ctrl.Result{}, fmt.Errorf("get deployment: %w", err)
	}
	var pods corev1.PodList
	if err := r.client.List(ctx, &pods, client.InNamespace(server.Namespace), client.MatchingLabels(mcpserverspec.SelectorLabels(server))); err != nil {
		return ctrl.Result{}, fmt.Errorf("list server pods: %w", err)
	}
	return ctrl.Result{}, r.writeStatus(ctx, server, observe(server, live, pods.Items))
}

// ensureSecret creates a generated Secret when it is missing and leaves it alone when it is not. An
// existing token is one an agent, or a running bot, may already hold.
func (r *Reconciler) ensureSecret(ctx context.Context, server *v1alpha1.MCPServer, name string, build func(*v1alpha1.MCPServer, string) *corev1.Secret) error {
	var existing corev1.Secret
	err := r.apiReader.Get(ctx, client.ObjectKey{Namespace: server.Namespace, Name: name}, &existing)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get secret %s: %w", name, err)
	}
	token, err := r.token()
	if err != nil {
		return fmt.Errorf("generate token: %w", err)
	}
	if err := r.client.Create(ctx, build(server, token)); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create secret %s: %w", name, err)
	}
	r.recorder.Eventf(server, corev1.EventTypeNormal, reasonTokenCreated, "created token secret %s", name)
	return nil
}

// removeRBAC takes back the Role and RoleBinding once provisioning is turned off. The cache holds only
// objects this operator labelled, so a same-named Role someone else made is never found here.
func (r *Reconciler) removeRBAC(ctx context.Context, server *v1alpha1.MCPServer) error {
	for _, obj := range []client.Object{&rbacv1.RoleBinding{}, &rbacv1.Role{}} {
		key := client.ObjectKey{Namespace: server.Namespace, Name: mcpserverspec.Name(server)}
		if err := r.client.Get(ctx, key, obj); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return fmt.Errorf("get %T %s: %w", obj, key.Name, err)
		}
		if owner := metav1.GetControllerOf(obj); owner == nil || owner.UID != server.UID {
			continue
		}
		if err := r.client.Delete(ctx, obj); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("delete %T %s: %w", obj, key.Name, err)
		}
	}
	return nil
}

// apply server-side applies a typed object. The fields it does not set stay whatever the API server
// defaulted, so a reconcile that changes nothing writes nothing.
func (r *Reconciler) apply(ctx context.Context, obj client.Object) error {
	content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return fmt.Errorf("convert %s: %w", obj.GetName(), err)
	}
	u := &unstructured.Unstructured{Object: content}
	delete(u.Object, "status")
	unstructured.RemoveNestedField(u.Object, "metadata", "creationTimestamp")
	unstructured.RemoveNestedField(u.Object, "spec", "template", "metadata", "creationTimestamp")

	if err := r.client.Apply(ctx, client.ApplyConfigurationFromUnstructured(u), client.FieldOwner(FieldOwner), client.ForceOwnership); err != nil {
		return fmt.Errorf("apply %s %s: %w", u.GetKind(), u.GetName(), err)
	}
	return nil
}

// observation is the one moment status reports twice: the phase a reader sees at a glance and the
// condition a controller waits on. Two answers to the same question is worse than either of them
// being wrong on its own, so only the phase is held and the condition is read off it. Holding both
// and building them together in one branch each would be the same thing only while every branch is
// in this function, and fail() already assembles one from somewhere else.
type observation struct {
	phase   v1alpha1.MCPServerPhase
	reason  string
	message string
}

// ready is what the phase amounts to for something waiting on this server. Running is the server
// accepting bots; every other phase is a reason it is not, and the reason says which.
func (o observation) ready(generation int64) metav1.Condition {
	status := metav1.ConditionFalse

	if o.phase == v1alpha1.MCPServerPhaseRunning {
		status = metav1.ConditionTrue
	}
	return metav1.Condition{
		Type:               v1alpha1.ConditionReady,
		Status:             status,
		Reason:             o.reason,
		Message:            o.message,
		ObservedGeneration: generation,
	}
}

// observe is Available once the Deployment has a pod up, with a nil deployment meaning there is
// none in view. Until then it names what is in the way: a pod the kubelet cannot bring up first,
// since a mistyped tag would otherwise read as "waiting for the server pod" for the ten minutes the
// Deployment takes to call it stalled.
func observe(server *v1alpha1.MCPServer, deployment *appsv1.Deployment, pods []corev1.Pod) observation {
	// A Deployment whose own controller has not caught up with its spec is reporting replicas from
	// the spec before it, and one of those is not the server the MCPServer asks for. It compares
	// the Deployment with itself and nothing here compares it with the MCPServer, so the reconcile
	// that follows an edit can still read the Deployment as it was before the apply; the watch on
	// it corrects that a moment later.
	if deployment != nil && deployment.Status.ObservedGeneration >= deployment.Generation && deployment.Status.AvailableReplicas > 0 {
		return observation{
			phase:   v1alpha1.MCPServerPhaseRunning,
			reason:  "Available",
			message: "the server is accepting bots",
		}
	}
	// A pod the kubelet cannot bring up comes first, since a mistyped tag would otherwise read as
	// "waiting for the server pod" for the ten minutes the Deployment takes to call it stalled.
	for i := range pods {
		if pods[i].DeletionTimestamp != nil {
			continue
		}
		if reason, message, ok := podstatus.Blocked(&pods[i]); ok {
			return observation{
				phase:   v1alpha1.MCPServerPhaseFailed,
				reason:  reason,
				message: fmt.Sprintf("pod %s: %s", pods[i].Name, message),
			}
		}
	}
	// Nothing in view is bringing a pod up, which is what Starting would claim: the apply has not
	// reached the cache yet, or the Deployment has been deleted out from under the server.
	if deployment == nil {
		return observation{
			phase:   v1alpha1.MCPServerPhasePending,
			reason:  "Progressing",
			message: "waiting for the server Deployment",
		}
	}
	for _, c := range deployment.Status.Conditions {
		// Progressing goes False only once the rollout has given up. Waiting longer will not
		// finish it, so this is Failed rather than a server still on its way up.
		if c.Type == appsv1.DeploymentProgressing && c.Status == corev1.ConditionFalse {
			return observation{phase: v1alpha1.MCPServerPhaseFailed, reason: c.Reason, message: c.Message}
		}
	}
	return observation{
		phase:   v1alpha1.MCPServerPhaseStarting,
		reason:  "Progressing",
		message: "waiting for the server pod to become ready",
	}
}

func (r *Reconciler) fail(ctx context.Context, server *v1alpha1.MCPServer, reason string, cause error) error {
	statusErr := r.writeStatus(ctx, server, observation{
		phase:   v1alpha1.MCPServerPhaseFailed,
		reason:  reason,
		message: cause.Error(),
	})
	if statusErr != nil {
		log.FromContext(ctx).Error(statusErr, "write status after failure")
	}
	return cause
}

func (r *Reconciler) writeStatus(ctx context.Context, server *v1alpha1.MCPServer, observed observation) error {
	token := mcpserverspec.TokenSecret(server)
	status := v1alpha1.MCPServerStatus{
		Phase:              observed.phase,
		Endpoint:           mcpserverspec.Endpoint(server),
		TokenSecretRef:     &token,
		Image:              mcpserverspec.Image(server, r.defaults),
		ObservedGeneration: server.Generation,
		Conditions:         append([]metav1.Condition(nil), server.Status.Conditions...),
	}
	meta.SetStatusCondition(&status.Conditions, observed.ready(server.Generation))
	if reflect.DeepEqual(server.Status, status) {
		return nil
	}

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latest v1alpha1.MCPServer
		if err := r.client.Get(ctx, client.ObjectKeyFromObject(server), &latest); err != nil {
			return err
		}
		if reflect.DeepEqual(latest.Status, status) {
			return nil
		}
		latest.Status = status
		return r.client.Status().Update(ctx, &latest)
	})
	if err := client.IgnoreNotFound(err); err != nil {
		return fmt.Errorf("update mcpserver status: %w", err)
	}
	return nil
}

func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
