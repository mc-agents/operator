// Package pool reconciles MinecraftBotPool resources into a set of
// ordinal-named MinecraftBots, and rolls their status back up into the counters
// that back the scale subresource.
package pool

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/mc-agents/operator/api/v1alpha1"
	"github.com/mc-agents/operator/internal/hash"
)

const (
	reasonScaledUp   = "ScaledUp"
	reasonScaledDown = "ScaledDown"
	reasonUpdated    = "TemplateUpdated"
	reasonNameClash  = "NameClash"
)

type Reconciler struct {
	client   client.Client
	recorder record.EventRecorder
}

func NewReconciler(c client.Client, recorder record.EventRecorder) *Reconciler {
	return &Reconciler{client: c, recorder: recorder}
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager, workers int) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("minecraftbotpool").
		For(&v1alpha1.MinecraftBotPool{}).
		Owns(&v1alpha1.MinecraftBot{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: workers}).
		Complete(r)
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var cached v1alpha1.MinecraftBotPool
	if err := r.client.Get(ctx, req.NamespacedName, &cached); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get minecraftbotpool: %w", err)
	}
	pool := cached.DeepCopy()

	if !pool.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	owned, err := r.ownedBots(ctx, pool)
	if err != nil {
		return ctrl.Result{}, err
	}

	desired := replicas(pool)
	templateHash := hash.JSON(pool.Spec.Template.Spec)

	byOrdinal := make(map[int]*v1alpha1.MinecraftBot, len(owned))
	var orphans []*v1alpha1.MinecraftBot
	for _, bot := range owned {
		ordinal, ok := ordinalOf(pool.Name, bot.Name)
		if !ok || ordinal >= desired {
			orphans = append(orphans, bot)
			continue
		}
		if existing, taken := byOrdinal[ordinal]; taken {
			orphans = append(orphans, existing)
		}
		byOrdinal[ordinal] = bot
	}

	for _, bot := range orphans {
		logger.Info("removing bot outside the pool size", "bot", bot.Name)
		err := r.client.Delete(ctx, bot, client.Preconditions{UID: &bot.UID})
		if err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
			return ctrl.Result{}, fmt.Errorf("delete bot %s: %w", bot.Name, err)
		}
		r.recorder.Eventf(pool, corev1.EventTypeNormal, reasonScaledDown, "deleted bot %s", bot.Name)
	}

	var clash string
	var requeue time.Duration
	for ordinal := range desired {
		existing, ok := byOrdinal[ordinal]
		if !ok {
			bot := newBot(pool, ordinal, templateHash)
			err := r.client.Create(ctx, bot)
			if apierrors.IsAlreadyExists(err) {
				if taken, err := r.foreign(ctx, bot); err != nil {
					return ctrl.Result{}, err
				} else if !taken {
					requeue = time.Second
					continue
				}
				message := fmt.Sprintf("bot %s already exists and is not owned by this pool", bot.Name)
				logger.Info("skipping an ordinal whose name is taken", "bot", bot.Name)
				// Once, on finding it: the clash stays until someone removes that bot, and every
				// resync would otherwise record it again.
				if pool.Status.LastError != message {
					r.recorder.Event(pool, corev1.EventTypeWarning, reasonNameClash, message)
				}
				if clash == "" {
					clash = message
				}
				continue
			}
			if err != nil {
				return ctrl.Result{}, fmt.Errorf("create bot %s: %w", bot.Name, err)
			}
			logger.Info("bot created", "bot", bot.Name)
			r.recorder.Eventf(pool, corev1.EventTypeNormal, reasonScaledUp, "created bot %s", bot.Name)
			continue
		}
		if existing.Annotations[v1alpha1.AnnotationSpecHash] == templateHash {
			continue
		}
		updated := existing.DeepCopy()
		updated.Spec = *pool.Spec.Template.Spec.DeepCopy()
		applyTemplateMetadata(updated, pool, templateHash)
		if err := r.client.Update(ctx, updated); err != nil {
			if apierrors.IsConflict(err) {
				return ctrl.Result{RequeueAfter: time.Second}, nil
			}
			return ctrl.Result{}, fmt.Errorf("update bot %s: %w", updated.Name, err)
		}
		r.recorder.Eventf(pool, corev1.EventTypeNormal, reasonUpdated, "updated bot %s", updated.Name)
	}

	if err := r.writeStatus(ctx, pool, desired, clash); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeue}, nil
}

// foreign tells whether the bot an AlreadyExists points at is somebody else's. Not in the cache
// yet is our own create racing the informer, and a bot owned by an earlier pool of this name is
// on its way out under the garbage collector: both are a short wait. Anything else stays where it
// is, and the pool says so rather than requeueing forever with nothing in status.
func (r *Reconciler) foreign(ctx context.Context, bot *v1alpha1.MinecraftBot) (bool, error) {
	var live v1alpha1.MinecraftBot
	if err := r.client.Get(ctx, client.ObjectKeyFromObject(bot), &live); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("get bot %s: %w", bot.Name, err)
	}
	owner := metav1.GetControllerOf(&live)
	ours := metav1.GetControllerOf(bot)
	if owner != nil && owner.Kind == ours.Kind && owner.Name == ours.Name {
		return false, nil
	}
	return true, nil
}

func (r *Reconciler) ownedBots(ctx context.Context, pool *v1alpha1.MinecraftBotPool) ([]*v1alpha1.MinecraftBot, error) {
	var list v1alpha1.MinecraftBotList
	err := r.client.List(ctx, &list,
		client.InNamespace(pool.Namespace),
		client.MatchingLabels{v1alpha1.LabelPool: pool.Name},
	)
	if err != nil {
		return nil, fmt.Errorf("list bots for pool %s: %w", pool.Name, err)
	}
	owned := make([]*v1alpha1.MinecraftBot, 0, len(list.Items))
	for i := range list.Items {
		bot := &list.Items[i]
		if owner := metav1.GetControllerOf(bot); owner != nil && owner.UID == pool.UID {
			owned = append(owned, bot)
		}
	}
	slices.SortFunc(owned, func(a, b *v1alpha1.MinecraftBot) int { return strings.Compare(a.Name, b.Name) })
	return owned, nil
}

func newBot(pool *v1alpha1.MinecraftBotPool, ordinal int, templateHash string) *v1alpha1.MinecraftBot {
	bot := &v1alpha1.MinecraftBot{
		ObjectMeta: metav1.ObjectMeta{
			Name:      botName(pool.Name, ordinal),
			Namespace: pool.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(pool, v1alpha1.SchemeGroupVersion.WithKind("MinecraftBotPool")),
			},
		},
		Spec: *pool.Spec.Template.Spec.DeepCopy(),
	}
	applyTemplateMetadata(bot, pool, templateHash)
	return bot
}

func applyTemplateMetadata(bot *v1alpha1.MinecraftBot, pool *v1alpha1.MinecraftBotPool, templateHash string) {
	if bot.Labels == nil {
		bot.Labels = map[string]string{}
	}
	maps.Copy(bot.Labels, pool.Spec.Template.Metadata.Labels)
	bot.Labels[v1alpha1.LabelPool] = pool.Name
	bot.Labels[v1alpha1.LabelManagedBy] = v1alpha1.ManagedByValue

	if bot.Annotations == nil {
		bot.Annotations = map[string]string{}
	}
	maps.Copy(bot.Annotations, pool.Spec.Template.Metadata.Annotations)
	bot.Annotations[v1alpha1.AnnotationSpecHash] = templateHash
}

// writeStatus rolls the bots up. lastError, when set, is the pool's own complaint and comes
// before any bot's.
func (r *Reconciler) writeStatus(ctx context.Context, pool *v1alpha1.MinecraftBotPool, desired int, lastError string) error {
	owned, err := r.ownedBots(ctx, pool)
	if err != nil {
		return err
	}

	status := v1alpha1.MinecraftBotPoolStatus{
		Replicas:           int32(len(owned)),
		Selector:           labels.SelectorFromSet(labels.Set{v1alpha1.LabelPool: pool.Name}).String(),
		LastError:          lastError,
		ObservedGeneration: pool.Generation,
		Conditions:         append([]metav1.Condition(nil), pool.Status.Conditions...),
	}
	for _, bot := range owned {
		if bot.Status.Phase == v1alpha1.BotPhaseRunning {
			status.ReadyReplicas++
		}
		if bot.Status.Link == v1alpha1.LinkStateLinked {
			status.LinkedReplicas++
		}
		if status.LastError == "" && bot.Status.LastError != "" {
			status.LastError = fmt.Sprintf("%s: %s", bot.Name, bot.Status.LastError)
		}
	}

	ready := metav1.ConditionFalse
	reason := "Scaling"
	message := fmt.Sprintf("%d/%d bots linked", status.LinkedReplicas, desired)
	if int(status.LinkedReplicas) == desired {
		ready = metav1.ConditionTrue
		reason = "AllLinked"
	}
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               v1alpha1.ConditionReady,
		Status:             ready,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: pool.Generation,
	})

	if reflect.DeepEqual(pool.Status, status) {
		return nil
	}

	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latest v1alpha1.MinecraftBotPool
		if err := r.client.Get(ctx, client.ObjectKeyFromObject(pool), &latest); err != nil {
			return err
		}
		if reflect.DeepEqual(latest.Status, status) {
			return nil
		}
		latest.Status = status
		return r.client.Status().Update(ctx, &latest)
	})
	if err := client.IgnoreNotFound(err); err != nil {
		return fmt.Errorf("update minecraftbotpool status: %w", err)
	}
	return nil
}

func replicas(pool *v1alpha1.MinecraftBotPool) int {
	if pool.Spec.Replicas == nil {
		return 1
	}
	if *pool.Spec.Replicas < 0 {
		return 0
	}
	return int(*pool.Spec.Replicas)
}

func botName(pool string, ordinal int) string {
	return fmt.Sprintf("%s-%d", pool, ordinal)
}

func ordinalOf(pool, bot string) (int, bool) {
	suffix, ok := strings.CutPrefix(bot, pool+"-")
	if !ok {
		return 0, false
	}
	ordinal, err := strconv.Atoi(suffix)
	if err != nil || ordinal < 0 {
		return 0, false
	}
	return ordinal, true
}
