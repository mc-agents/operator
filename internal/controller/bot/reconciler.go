// Package bot reconciles MinecraftBot resources into the single pod that runs
// the bot process, and mirrors what that pod is doing back into status.
package bot

import (
	"context"
	"fmt"
	"reflect"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/mc-agents/operator/api/v1alpha1"
	"github.com/mc-agents/operator/internal/podspec"
	"github.com/mc-agents/operator/internal/profile"
	"github.com/mc-agents/operator/internal/throttle"
)

const (
	reasonSpawned    = "Spawned"
	reasonRecreating = "Recreating"
	reasonThrottled  = "Throttled"
	reasonInvalid    = "InvalidSpec"
	reasonAdopted    = "AdoptionRefused"
)

const (
	predecessorMessage = "waiting for the previous pod to terminate"
	predecessorRequeue = 2 * time.Second
)

type Reconciler struct {
	client   client.Client
	recorder record.EventRecorder

	// apiReader bypasses the cache. The pod cache is filtered to pods this
	// operator manages, so a name clash has to be confirmed against the API
	// server or a pod we created moments ago would read as someone else's.
	apiReader client.Reader

	builder podspec.Builder
	gate    throttle.Gate
	now     func() time.Time
}

func NewReconciler(c client.Client, apiReader client.Reader, recorder record.EventRecorder, builder podspec.Builder, gate throttle.Gate) *Reconciler {
	return &Reconciler{
		client:    c,
		apiReader: apiReader,
		recorder:  recorder,
		builder:   builder,
		gate:      gate,
		now:       time.Now,
	}
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager, workers int) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("minecraftbot").
		For(&v1alpha1.MinecraftBot{}).
		Owns(&corev1.Pod{}).
		Watches(&v1alpha1.MinecraftBotProfile{}, handler.EnqueueRequestsFromMapFunc(r.botsIn)).
		Watches(&v1alpha1.ClusterMinecraftBotProfile{}, handler.EnqueueRequestsFromMapFunc(r.botsIn)).
		WithOptions(controller.Options{MaxConcurrentReconciles: workers}).
		Complete(r)
}

// botsIn enqueues every bot a changed profile could reach: the namespace's for a MinecraftBotProfile,
// all of them for a ClusterMinecraftBotProfile. A profile changes rarely and a bot it does not reach
// reconciles to nothing.
func (r *Reconciler) botsIn(ctx context.Context, obj client.Object) []reconcile.Request {
	var bots v1alpha1.MinecraftBotList
	if err := r.client.List(ctx, &bots, client.InNamespace(obj.GetNamespace())); err != nil {
		log.FromContext(ctx).Error(err, "list bots for profile", "profile", obj.GetName())
		return nil
	}
	requests := make([]reconcile.Request, 0, len(bots.Items))
	for _, bot := range bots.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&bot)})
	}
	return requests
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var cached v1alpha1.MinecraftBot
	if err := r.client.Get(ctx, req.NamespacedName, &cached); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get minecraftbot: %w", err)
	}
	bot := cached.DeepCopy()

	if !bot.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.writeStatus(ctx, bot, v1alpha1.MinecraftBotStatus{
			Phase:              v1alpha1.BotPhaseTerminating,
			Link:               v1alpha1.LinkStateUnknown,
			PodName:            bot.Status.PodName,
			ObservedGeneration: bot.Generation,
		})
	}

	resolved, err := profile.Lookup(ctx, r.client, bot)
	if err != nil {
		return ctrl.Result{}, r.reportInvalid(ctx, bot, err)
	}
	desired, err := r.builder.Build(bot, resolved)
	if err != nil {
		return ctrl.Result{}, r.reportInvalid(ctx, bot, err)
	}
	return r.converge(ctx, bot, desired, resolved.Sources)
}

func (r *Reconciler) reportInvalid(ctx context.Context, bot *v1alpha1.MinecraftBot, err error) error {
	r.recorder.Eventf(bot, corev1.EventTypeWarning, reasonInvalid, "%v", err)
	return r.writeStatus(ctx, bot, v1alpha1.MinecraftBotStatus{
		Phase:              v1alpha1.BotPhaseFailed,
		Link:               v1alpha1.LinkStateUnknown,
		LastError:          err.Error(),
		ObservedGeneration: bot.Generation,
	})
}

func (r *Reconciler) converge(ctx context.Context, bot *v1alpha1.MinecraftBot, desired *corev1.Pod, profiles []string) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var pod corev1.Pod
	switch err := r.client.Get(ctx, client.ObjectKeyFromObject(bot), &pod); {
	case apierrors.IsNotFound(err):
		return r.spawn(ctx, bot, desired, profiles)
	case err != nil:
		return ctrl.Result{}, fmt.Errorf("get pod: %w", err)
	}

	switch owner := metav1.GetControllerOf(&pod); {
	case isPredecessor(owner, bot):
		return r.awaitPredecessor(ctx, bot, &pod)
	case owner == nil || owner.UID != bot.UID:
		return ctrl.Result{}, r.reportClash(ctx, bot, pod.Name)
	}

	if pod.DeletionTimestamp.IsZero() && needsReplacement(&pod, desired) {
		logger.Info("replacing pod", "pod", pod.Name)
		r.recorder.Eventf(bot, corev1.EventTypeNormal, reasonRecreating,
			"pod %s no longer matches the spec", pod.Name)
		if err := r.deletePod(ctx, &pod); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}

	observed := Observe(&pod)
	return ctrl.Result{}, r.writeStatus(ctx, bot, v1alpha1.MinecraftBotStatus{
		Phase:              observed.Phase,
		Link:               observed.Link,
		PodName:            pod.Name,
		Image:              containerImage(&pod),
		Profiles:           profiles,
		LastError:          observed.Message,
		LastSpawnTime:      bot.Status.LastSpawnTime,
		ObservedGeneration: bot.Generation,
	})
}

func (r *Reconciler) spawn(ctx context.Context, bot *v1alpha1.MinecraftBot, desired *corev1.Pod, profiles []string) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if ok, wait := r.gate.Acquire(r.now()); !ok {
		logger.V(2).Info("spawn throttled", "wait", wait)
		r.recorder.Eventf(bot, corev1.EventTypeNormal, reasonThrottled,
			"waiting %s before creating the pod", wait.Round(time.Millisecond))
		if err := r.writeStatus(ctx, bot, v1alpha1.MinecraftBotStatus{
			Phase:              v1alpha1.BotPhasePending,
			Link:               v1alpha1.LinkStateUnknown,
			LastSpawnTime:      bot.Status.LastSpawnTime,
			ObservedGeneration: bot.Generation,
		}); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: wait}, nil
	}

	err := r.client.Create(ctx, desired)
	if apierrors.IsAlreadyExists(err) {
		return r.confirmClash(ctx, bot, desired.Name)
	}
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("create pod: %w", err)
	}
	logger.Info("pod created", "pod", desired.Name, "image", containerImage(desired))
	r.recorder.Eventf(bot, corev1.EventTypeNormal, reasonSpawned, "created pod %s", desired.Name)

	now := metav1.NewTime(r.now())
	return ctrl.Result{}, r.writeStatus(ctx, bot, v1alpha1.MinecraftBotStatus{
		Phase:              v1alpha1.BotPhaseStarting,
		Link:               v1alpha1.LinkStateUnknown,
		PodName:            desired.Name,
		Image:              containerImage(desired),
		Profiles:           profiles,
		LastSpawnTime:      &now,
		ObservedGeneration: bot.Generation,
	})
}

// confirmClash re-reads the pod straight from the API server. A create that
// raced our own cache is not a clash, nor is the pod of the bot this one
// replaces; anything else is.
func (r *Reconciler) confirmClash(ctx context.Context, bot *v1alpha1.MinecraftBot, name string) (ctrl.Result, error) {
	var live corev1.Pod
	key := client.ObjectKey{Namespace: bot.Namespace, Name: name}
	if err := r.apiReader.Get(ctx, key, &live); err == nil {
		switch owner := metav1.GetControllerOf(&live); {
		case owner != nil && owner.UID == bot.UID:
			return ctrl.Result{}, nil
		case isPredecessor(owner, bot):
			return r.awaitPredecessor(ctx, bot, &live)
		}
	}
	return ctrl.Result{}, r.reportClash(ctx, bot, name)
}

// isPredecessor tells the pod of an earlier MinecraftBot of this name from a
// foreign one. Two bots cannot share a name, so an owner with this name and
// another UID has been deleted; its pod is on its way out, or will be once the
// garbage collector reaches it.
func isPredecessor(owner *metav1.OwnerReference, bot *v1alpha1.MinecraftBot) bool {
	return owner != nil && owner.Kind == "MinecraftBot" && owner.Name == bot.Name && owner.UID != bot.UID
}

// awaitPredecessor holds the bot in Pending until the previous bot's pod is
// gone, deleting it when the garbage collector has not yet. leave-server then
// join-server under one name lands here for the seconds the old process takes
// to exit; the MCP server keeps polling on Pending, where Failed made it give
// the bot back.
func (r *Reconciler) awaitPredecessor(ctx context.Context, bot *v1alpha1.MinecraftBot, pod *corev1.Pod) (ctrl.Result, error) {
	if pod.DeletionTimestamp.IsZero() {
		log.FromContext(ctx).Info("deleting the previous bot's pod", "pod", pod.Name)
		if err := r.deletePod(ctx, pod); err != nil {
			return ctrl.Result{}, err
		}
	}
	if err := r.writeStatus(ctx, bot, v1alpha1.MinecraftBotStatus{
		Phase:              v1alpha1.BotPhasePending,
		Link:               v1alpha1.LinkStateUnknown,
		LastError:          predecessorMessage,
		LastSpawnTime:      bot.Status.LastSpawnTime,
		ObservedGeneration: bot.Generation,
	}); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: predecessorRequeue}, nil
}

func (r *Reconciler) reportClash(ctx context.Context, bot *v1alpha1.MinecraftBot, name string) error {
	message := fmt.Sprintf("pod %s already exists and is not owned by this bot", name)
	r.recorder.Event(bot, corev1.EventTypeWarning, reasonAdopted, message)
	return r.writeStatus(ctx, bot, v1alpha1.MinecraftBotStatus{
		Phase:              v1alpha1.BotPhaseFailed,
		Link:               v1alpha1.LinkStateUnknown,
		PodName:            name,
		LastError:          message,
		ObservedGeneration: bot.Generation,
	})
}

func needsReplacement(pod, desired *corev1.Pod) bool {
	if pod.Annotations[v1alpha1.AnnotationSpecHash] != desired.Annotations[v1alpha1.AnnotationSpecHash] {
		return true
	}
	return pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed
}

func (r *Reconciler) deletePod(ctx context.Context, pod *corev1.Pod) error {
	err := r.client.Delete(ctx, pod, client.Preconditions{UID: &pod.UID})
	if err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
		return fmt.Errorf("delete pod %s: %w", pod.Name, err)
	}
	return nil
}

// writeStatus re-fetches before each attempt so the update lands on the latest
// resourceVersion, and skips the call entirely when nothing moved.
func (r *Reconciler) writeStatus(ctx context.Context, bot *v1alpha1.MinecraftBot, status v1alpha1.MinecraftBotStatus) error {
	status.Conditions = append([]metav1.Condition(nil), bot.Status.Conditions...)
	applyConditions(&status)

	if reflect.DeepEqual(bot.Status, status) {
		return nil
	}

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latest v1alpha1.MinecraftBot
		if err := r.client.Get(ctx, client.ObjectKeyFromObject(bot), &latest); err != nil {
			return err
		}
		if reflect.DeepEqual(latest.Status, status) {
			return nil
		}
		latest.Status = status
		return r.client.Status().Update(ctx, &latest)
	})
	if err != nil {
		return fmt.Errorf("update minecraftbot status: %w", err)
	}
	return nil
}

func applyConditions(status *v1alpha1.MinecraftBotStatus) {
	ready := metav1.ConditionFalse
	reason := string(status.Phase)
	message := status.LastError
	if status.Link == v1alpha1.LinkStateLinked {
		ready = metav1.ConditionTrue
		reason = "Linked"
		message = "bot is connected to the MCP server"
	}
	if reason == "" {
		reason = "Unknown"
	}
	if message == "" {
		message = fmt.Sprintf("link is %s", linkOrUnknown(status.Link))
	}
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               v1alpha1.ConditionReady,
		Status:             ready,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: status.ObservedGeneration,
	})

	degraded := metav1.ConditionFalse
	degradedReason := "Healthy"
	degradedMessage := "no error reported"
	if status.Phase == v1alpha1.BotPhaseFailed {
		degraded = metav1.ConditionTrue
		degradedReason = "PodFailed"
		degradedMessage = status.LastError
		if degradedMessage == "" {
			degradedMessage = "pod is not running"
		}
	}
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               v1alpha1.ConditionDegraded,
		Status:             degraded,
		Reason:             degradedReason,
		Message:            degradedMessage,
		ObservedGeneration: status.ObservedGeneration,
	})
}

func linkOrUnknown(state v1alpha1.LinkState) v1alpha1.LinkState {
	if state == "" {
		return v1alpha1.LinkStateUnknown
	}
	return state
}

func containerImage(pod *corev1.Pod) string {
	for _, c := range pod.Spec.Containers {
		if c.Name == podspec.ContainerName {
			return c.Image
		}
	}
	return ""
}
