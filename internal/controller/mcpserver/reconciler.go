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

	var live appsv1.Deployment
	if err := r.client.Get(ctx, client.ObjectKeyFromObject(deployment), &live); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("get deployment: %w", err)
	}
	var pods corev1.PodList
	if err := r.client.List(ctx, &pods, client.InNamespace(server.Namespace), client.MatchingLabels(mcpserverspec.SelectorLabels(server))); err != nil {
		return ctrl.Result{}, fmt.Errorf("list server pods: %w", err)
	}
	return ctrl.Result{}, r.writeStatus(ctx, server, readyCondition(server, &live, pods.Items))
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

// readyCondition is Available once the Deployment has a pod up. Until then it names what is in the
// way: a pod the kubelet cannot bring up first, since a mistyped tag would otherwise read as
// "waiting for the server pod" for the ten minutes the Deployment takes to call it stalled.
func readyCondition(server *v1alpha1.MCPServer, deployment *appsv1.Deployment, pods []corev1.Pod) metav1.Condition {
	condition := metav1.Condition{
		Type:               v1alpha1.ConditionReady,
		Status:             metav1.ConditionFalse,
		Reason:             "Progressing",
		Message:            "waiting for the server pod to become ready",
		ObservedGeneration: server.Generation,
	}
	if deployment.Generation != 0 && deployment.Status.ObservedGeneration >= deployment.Generation && deployment.Status.AvailableReplicas > 0 {
		condition.Status = metav1.ConditionTrue
		condition.Reason = "Available"
		condition.Message = "the server is accepting bots"
		return condition
	}
	for i := range pods {
		if pods[i].DeletionTimestamp != nil {
			continue
		}
		if reason, message, ok := podstatus.Blocked(&pods[i]); ok {
			condition.Reason = reason
			condition.Message = fmt.Sprintf("pod %s: %s", pods[i].Name, message)
			return condition
		}
	}
	for _, c := range deployment.Status.Conditions {
		if c.Type == appsv1.DeploymentProgressing && c.Status == corev1.ConditionFalse {
			condition.Reason = c.Reason
			condition.Message = c.Message
		}
	}
	return condition
}

func (r *Reconciler) fail(ctx context.Context, server *v1alpha1.MCPServer, reason string, cause error) error {
	statusErr := r.writeStatus(ctx, server, metav1.Condition{
		Type:               v1alpha1.ConditionReady,
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            cause.Error(),
		ObservedGeneration: server.Generation,
	})
	if statusErr != nil {
		log.FromContext(ctx).Error(statusErr, "write status after failure")
	}
	return cause
}

func (r *Reconciler) writeStatus(ctx context.Context, server *v1alpha1.MCPServer, ready metav1.Condition) error {
	token := mcpserverspec.TokenSecret(server)
	status := v1alpha1.MCPServerStatus{
		Endpoint:           mcpserverspec.Endpoint(server),
		TokenSecretRef:     &token,
		Image:              mcpserverspec.Image(server, r.defaults),
		ObservedGeneration: server.Generation,
		Conditions:         append([]metav1.Condition(nil), server.Status.Conditions...),
	}
	meta.SetStatusCondition(&status.Conditions, ready)
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
