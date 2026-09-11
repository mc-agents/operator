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
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/mc-agents/operator/api/v1alpha1"
	"github.com/mc-agents/operator/internal/podspec"
	"github.com/mc-agents/operator/internal/throttle"
)

const (
	reasonSpawned    = "Spawned"
	reasonRecreating = "Recreating"
	reasonThrottled  = "Throttled"
	reasonInvalid    = "InvalidSpec"
	reasonAdopted    = "AdoptionRefused"
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
		WithOptions(controller.Options{MaxConcurrentReconciles: workers}).
		Complete(r)
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

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

	desired, err := r.builder.Build(bot)
	if err != nil {
		r.recorder.Eventf(bot, corev1.EventTypeWarning, reasonInvalid, "%v", err)
		return ctrl.Result{}, r.writeStatus(ctx, bot, v1alpha1.MinecraftBotStatus{
			Phase:              v1alpha1.BotPhaseFailed,
			Link:               v1alpha1.LinkStateUnknown,
			LastError:          err.Error(),
			ObservedGeneration: bot.Generation,
		})
	}

	var pod corev1.Pod
	switch err := r.client.Get(ctx, req.NamespacedName, &pod); {
	case apierrors.IsNotFound(err):
		return r.spawn(ctx, bot, desired)
	case err != nil:
		return ctrl.Result{}, fmt.Errorf("get pod: %w", err)
	}

	if owner := metav1.GetControllerOf(&pod); owner == nil || owner.UID != bot.UID {
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
		LastError:          observed.Message,
		LastSpawnTime:      bot.Status.LastSpawnTime,
		ObservedGeneration: bot.Generation,
	})
}

func (r *Reconciler) spawn(ctx context.Context, bot *v1alpha1.MinecraftBot, desired *corev1.Pod) (ctrl.Result, error) {
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
		return ctrl.Result{}, r.confirmClash(ctx, bot, desired.Name)
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
		LastSpawnTime:      &now,
		ObservedGeneration: bot.Generation,
	})
}

// confirmClash re-reads the pod straight from the API server. A create that
// raced our own cache is not a clash; anything else is.
func (r *Reconciler) confirmClash(ctx context.Context, bot *v1alpha1.MinecraftBot, name string) error {
	var live corev1.Pod
	key := client.ObjectKey{Namespace: bot.Namespace, Name: name}
	if err := r.apiReader.Get(ctx, key, &live); err == nil {
		if owner := metav1.GetControllerOf(&live); owner != nil && owner.UID == bot.UID {
			return nil
		}
	}
	return r.reportClash(ctx, bot, name)
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
