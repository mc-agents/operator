package pool

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"

	"github.com/mc-agents/operator/api/v1alpha1"
	"github.com/mc-agents/operator/pkg/apiclient"
	"github.com/mc-agents/operator/pkg/hash"
	"github.com/mc-agents/operator/pkg/queue"
)

const (
	reasonScaledUp   = "ScaledUp"
	reasonScaledDown = "ScaledDown"
	reasonUpdated    = "TemplateUpdated"
)

type Reconciler struct {
	pools      apiclient.Resource[*v1alpha1.MinecraftBotPool]
	poolLister apiclient.Lister[*v1alpha1.MinecraftBotPool]
	bots       apiclient.Resource[*v1alpha1.MinecraftBot]
	botLister  apiclient.Lister[*v1alpha1.MinecraftBot]
	recorder   record.EventRecorder
}

func NewReconciler(
	pools apiclient.Resource[*v1alpha1.MinecraftBotPool],
	poolLister apiclient.Lister[*v1alpha1.MinecraftBotPool],
	bots apiclient.Resource[*v1alpha1.MinecraftBot],
	botLister apiclient.Lister[*v1alpha1.MinecraftBot],
	recorder record.EventRecorder,
) *Reconciler {
	return &Reconciler{
		pools:      pools,
		poolLister: poolLister,
		bots:       bots,
		botLister:  botLister,
		recorder:   recorder,
	}
}

func (r *Reconciler) Reconcile(ctx context.Context, key types.NamespacedName) (queue.Result, error) {
	logger := klog.FromContext(ctx)

	cached, err := r.poolLister.Get(key.Namespace, key.Name)
	if apierrors.IsNotFound(err) {
		return queue.Result{}, nil
	}
	if err != nil {
		return queue.Result{}, fmt.Errorf("get minecraftbotpool: %w", err)
	}
	pool := cached.DeepCopy()

	if pool.DeletionTimestamp != nil {
		return queue.Result{}, nil
	}

	owned, err := r.ownedBots(pool)
	if err != nil {
		return queue.Result{}, err
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
		if err := r.bots.Delete(ctx, bot.Namespace, bot.Name, metav1.DeleteOptions{
			Preconditions: &metav1.Preconditions{UID: &bot.UID},
		}); err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
			return queue.Result{}, fmt.Errorf("delete bot %s: %w", bot.Name, err)
		}
		r.recorder.Eventf(pool, corev1.EventTypeNormal, reasonScaledDown, "deleted bot %s", bot.Name)
	}

	for ordinal := 0; ordinal < desired; ordinal++ {
		existing, ok := byOrdinal[ordinal]
		if !ok {
			bot := r.newBot(pool, ordinal, templateHash)
			created, err := r.bots.Create(ctx, bot)
			if apierrors.IsAlreadyExists(err) {
				return queue.Result{RequeueAfter: time.Second}, nil
			}
			if err != nil {
				return queue.Result{}, fmt.Errorf("create bot %s: %w", bot.Name, err)
			}
			logger.Info("bot created", "bot", created.Name)
			r.recorder.Eventf(pool, corev1.EventTypeNormal, reasonScaledUp, "created bot %s", created.Name)
			continue
		}
		if existing.Annotations[v1alpha1.AnnotationSpecHash] == templateHash {
			continue
		}
		updated := existing.DeepCopy()
		updated.Spec = *pool.Spec.Template.Spec.DeepCopy()
		applyTemplateMetadata(updated, pool, templateHash)
		if _, err := r.bots.Update(ctx, updated); err != nil {
			if apierrors.IsConflict(err) {
				return queue.Result{RequeueAfter: time.Second}, nil
			}
			return queue.Result{}, fmt.Errorf("update bot %s: %w", updated.Name, err)
		}
		r.recorder.Eventf(pool, corev1.EventTypeNormal, reasonUpdated, "updated bot %s", updated.Name)
	}

	return queue.Result{}, r.writeStatus(ctx, pool, desired)
}

func (r *Reconciler) ownedBots(pool *v1alpha1.MinecraftBotPool) ([]*v1alpha1.MinecraftBot, error) {
	selector := labels.SelectorFromSet(labels.Set{v1alpha1.LabelPool: pool.Name})
	candidates, err := r.botLister.List(pool.Namespace, selector)
	if err != nil {
		return nil, fmt.Errorf("list bots for pool %s: %w", pool.Name, err)
	}
	owned := make([]*v1alpha1.MinecraftBot, 0, len(candidates))
	for _, bot := range candidates {
		if owner := metav1.GetControllerOf(bot); owner != nil && owner.UID == pool.UID {
			owned = append(owned, bot)
		}
	}
	sort.Slice(owned, func(i, j int) bool { return owned[i].Name < owned[j].Name })
	return owned, nil
}

func (r *Reconciler) newBot(pool *v1alpha1.MinecraftBotPool, ordinal int, templateHash string) *v1alpha1.MinecraftBot {
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

func (r *Reconciler) writeStatus(ctx context.Context, pool *v1alpha1.MinecraftBotPool, desired int) error {
	owned, err := r.ownedBots(pool)
	if err != nil {
		return err
	}

	status := v1alpha1.MinecraftBotPoolStatus{
		Replicas:           int32(len(owned)),
		Selector:           labels.SelectorFromSet(labels.Set{v1alpha1.LabelPool: pool.Name}).String(),
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

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest, err := r.pools.Get(ctx, pool.Namespace, pool.Name)
		if err != nil {
			return err
		}
		if reflect.DeepEqual(latest.Status, status) {
			return nil
		}
		latest.Status = status
		_, err = r.pools.UpdateStatus(ctx, latest)
		return err
	})
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
