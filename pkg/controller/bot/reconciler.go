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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	corev1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"

	"github.com/mc-agents/operator/api/v1alpha1"
	"github.com/mc-agents/operator/pkg/apiclient"
	"github.com/mc-agents/operator/pkg/podspec"
	"github.com/mc-agents/operator/pkg/queue"
	"github.com/mc-agents/operator/pkg/throttle"
)

const (
	reasonSpawned    = "Spawned"
	reasonRecreating = "Recreating"
	reasonThrottled  = "Throttled"
	reasonInvalid    = "InvalidSpec"
	reasonAdopted    = "AdoptionRefused"
)

type Reconciler struct {
	bots     apiclient.Resource[*v1alpha1.MinecraftBot]
	lister   apiclient.Lister[*v1alpha1.MinecraftBot]
	pods     kubernetes.Interface
	podsFrom corev1listers.PodLister
	builder  podspec.Builder
	gate     throttle.Gate
	recorder record.EventRecorder
	now      func() time.Time
}

func NewReconciler(
	bots apiclient.Resource[*v1alpha1.MinecraftBot],
	lister apiclient.Lister[*v1alpha1.MinecraftBot],
	pods kubernetes.Interface,
	podLister corev1listers.PodLister,
	builder podspec.Builder,
	gate throttle.Gate,
	recorder record.EventRecorder,
) *Reconciler {
	return &Reconciler{
		bots:     bots,
		lister:   lister,
		pods:     pods,
		podsFrom: podLister,
		builder:  builder,
		gate:     gate,
		recorder: recorder,
		now:      time.Now,
	}
}

func (r *Reconciler) Reconcile(ctx context.Context, key types.NamespacedName) (queue.Result, error) {
	logger := klog.FromContext(ctx)

	cached, err := r.lister.Get(key.Namespace, key.Name)
	if apierrors.IsNotFound(err) {
		return queue.Result{}, nil
	}
	if err != nil {
		return queue.Result{}, fmt.Errorf("get minecraftbot: %w", err)
	}
	bot := cached.DeepCopy()

	if bot.DeletionTimestamp != nil {
		return queue.Result{}, r.writeStatus(ctx, bot, v1alpha1.MinecraftBotStatus{
			Phase:              v1alpha1.BotPhaseTerminating,
			Link:               v1alpha1.LinkStateUnknown,
			PodName:            bot.Status.PodName,
			ObservedGeneration: bot.Generation,
		})
	}

	desired, err := r.builder.Build(bot)
	if err != nil {
		r.recorder.Eventf(bot, corev1.EventTypeWarning, reasonInvalid, "%v", err)
		return queue.Result{}, r.writeStatus(ctx, bot, v1alpha1.MinecraftBotStatus{
			Phase:              v1alpha1.BotPhaseFailed,
			Link:               v1alpha1.LinkStateUnknown,
			LastError:          err.Error(),
			ObservedGeneration: bot.Generation,
		})
	}

	pod, err := r.podsFrom.Pods(key.Namespace).Get(key.Name)
	switch {
	case apierrors.IsNotFound(err):
		return r.spawn(ctx, bot, desired)
	case err != nil:
		return queue.Result{}, fmt.Errorf("get pod: %w", err)
	}

	if owner := metav1.GetControllerOf(pod); owner == nil || owner.UID != bot.UID {
		r.recorder.Eventf(bot, corev1.EventTypeWarning, reasonAdopted,
			"pod %s already exists and is not owned by this bot", pod.Name)
		return queue.Result{}, r.writeStatus(ctx, bot, v1alpha1.MinecraftBotStatus{
			Phase:              v1alpha1.BotPhaseFailed,
			Link:               v1alpha1.LinkStateUnknown,
			PodName:            pod.Name,
			LastError:          fmt.Sprintf("pod %s is owned by something else", pod.Name),
			ObservedGeneration: bot.Generation,
		})
	}

	if pod.DeletionTimestamp == nil && r.needsReplacement(pod, desired) {
		logger.Info("replacing pod", "pod", pod.Name)
		r.recorder.Eventf(bot, corev1.EventTypeNormal, reasonRecreating,
			"pod %s no longer matches the spec", pod.Name)
		if err := r.deletePod(ctx, pod); err != nil {
			return queue.Result{}, err
		}
		return queue.Result{RequeueAfter: time.Second}, nil
	}

	observed := Observe(pod)
	return queue.Result{}, r.writeStatus(ctx, bot, v1alpha1.MinecraftBotStatus{
		Phase:              observed.Phase,
		Link:               observed.Link,
		PodName:            pod.Name,
		Image:              containerImage(pod),
		LastError:          observed.Message,
		LastSpawnTime:      bot.Status.LastSpawnTime,
		ObservedGeneration: bot.Generation,
	})
}

func (r *Reconciler) spawn(ctx context.Context, bot *v1alpha1.MinecraftBot, desired *corev1.Pod) (queue.Result, error) {
	logger := klog.FromContext(ctx)

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
			return queue.Result{}, err
		}
		return queue.Result{RequeueAfter: wait}, nil
	}

	created, err := r.pods.CoreV1().Pods(desired.Namespace).Create(ctx, desired, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return queue.Result{}, r.reportNameClash(ctx, bot, desired.Name)
	}
	if err != nil {
		return queue.Result{}, fmt.Errorf("create pod: %w", err)
	}
	logger.Info("pod created", "pod", created.Name, "image", containerImage(created))
	r.recorder.Eventf(bot, corev1.EventTypeNormal, reasonSpawned, "created pod %s", created.Name)

	now := metav1.NewTime(r.now())
	return queue.Result{}, r.writeStatus(ctx, bot, v1alpha1.MinecraftBotStatus{
		Phase:              v1alpha1.BotPhaseStarting,
		Link:               v1alpha1.LinkStateUnknown,
		PodName:            created.Name,
		Image:              containerImage(created),
		LastSpawnTime:      &now,
		ObservedGeneration: bot.Generation,
	})
}

func (r *Reconciler) reportNameClash(ctx context.Context, bot *v1alpha1.MinecraftBot, name string) error {
	live, err := r.pods.CoreV1().Pods(bot.Namespace).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		if owner := metav1.GetControllerOf(live); owner != nil && owner.UID == bot.UID {
			return nil
		}
	}
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

func (r *Reconciler) needsReplacement(pod, desired *corev1.Pod) bool {
	if pod.Annotations[v1alpha1.AnnotationSpecHash] != desired.Annotations[v1alpha1.AnnotationSpecHash] {
		return true
	}
	return pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed
}

func (r *Reconciler) deletePod(ctx context.Context, pod *corev1.Pod) error {
	opts := metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &pod.UID},
	}
	err := r.pods.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, opts)
	if err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
		return fmt.Errorf("delete pod %s: %w", pod.Name, err)
	}
	return nil
}

func (r *Reconciler) writeStatus(ctx context.Context, bot *v1alpha1.MinecraftBot, status v1alpha1.MinecraftBotStatus) error {
	status.Conditions = append([]metav1.Condition(nil), bot.Status.Conditions...)
	applyConditions(&status)

	if reflect.DeepEqual(bot.Status, status) {
		return nil
	}

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest, err := r.bots.Get(ctx, bot.Namespace, bot.Name)
		if err != nil {
			return err
		}
		if reflect.DeepEqual(latest.Status, status) {
			return nil
		}
		latest.Status = status
		_, err = r.bots.UpdateStatus(ctx, latest)
		return err
	})
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
