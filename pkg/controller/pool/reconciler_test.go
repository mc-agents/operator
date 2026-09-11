package pool_test

import (
	"context"
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"

	"github.com/mc-agents/operator/api/v1alpha1"
	"github.com/mc-agents/operator/pkg/apiclient/fake"
	poolctl "github.com/mc-agents/operator/pkg/controller/pool"
)

const (
	namespace = "qa"
	poolName  = "scouts"
	poolUID   = types.UID("pool-uid")
)

type harness struct {
	pools *fake.Resource[*v1alpha1.MinecraftBotPool]
	bots  *fake.Resource[*v1alpha1.MinecraftBot]
	under *poolctl.Reconciler
}

func newHarness(t *testing.T, replicas int32) *harness {
	t.Helper()

	pools := fake.NewResource[*v1alpha1.MinecraftBotPool](v1alpha1.Resource("minecraftbotpools"))
	bots := fake.NewResource[*v1alpha1.MinecraftBot](v1alpha1.Resource("minecraftbots"))
	pools.Seed(&v1alpha1.MinecraftBotPool{
		ObjectMeta: metav1.ObjectMeta{Name: poolName, Namespace: namespace, UID: poolUID},
		Spec: v1alpha1.MinecraftBotPoolSpec{
			Replicas: &replicas,
			Template: v1alpha1.MinecraftBotTemplate{
				Spec: v1alpha1.MinecraftBotSpec{
					Kind:             v1alpha1.BotKindMineflayer,
					MinecraftVersion: "26.1.2",
					Server:           v1alpha1.MCPServerRef{Host: "mcp.qa.svc", Port: 8765},
				},
			},
		},
	})

	return &harness{
		pools: pools,
		bots:  bots,
		under: poolctl.NewReconciler(pools, pools.Lister(), bots, bots.Lister(), record.NewFakeRecorder(32)),
	}
}

func (h *harness) reconcile(t *testing.T) {
	t.Helper()
	if _, err := h.under.Reconcile(context.Background(), types.NamespacedName{Namespace: namespace, Name: poolName}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
}

func (h *harness) setReplicas(t *testing.T, replicas int32) {
	t.Helper()
	pool, err := h.pools.Get(context.Background(), namespace, poolName)
	if err != nil {
		t.Fatalf("get pool: %v", err)
	}
	pool.Spec.Replicas = &replicas
	if _, err := h.pools.Update(context.Background(), pool); err != nil {
		t.Fatalf("update pool: %v", err)
	}
}

func TestPoolCreatesOrdinalBots(t *testing.T) {
	h := newHarness(t, 3)
	h.reconcile(t)

	want := []string{"scouts-0", "scouts-1", "scouts-2"}
	if got := h.bots.Names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("bots are %v, want %v", got, want)
	}

	bot, err := h.bots.Get(context.Background(), namespace, "scouts-0")
	if err != nil {
		t.Fatalf("get bot: %v", err)
	}
	if bot.Labels[v1alpha1.LabelPool] != poolName {
		t.Errorf("bot is not labelled with its pool: %v", bot.Labels)
	}
	if owner := metav1.GetControllerOf(bot); owner == nil || owner.UID != poolUID {
		t.Error("bot must be owned by the pool so scale-down and deletion collect it")
	}
}

func TestPoolIsIdempotent(t *testing.T) {
	h := newHarness(t, 2)
	h.reconcile(t)
	first, err := h.bots.Get(context.Background(), namespace, "scouts-1")
	if err != nil {
		t.Fatalf("get bot: %v", err)
	}

	h.reconcile(t)
	second, err := h.bots.Get(context.Background(), namespace, "scouts-1")
	if err != nil {
		t.Fatalf("get bot: %v", err)
	}
	if first.UID != second.UID {
		t.Fatal("a second reconcile replaced a bot that was already correct")
	}
}

func TestPoolScalesDownFromTheTop(t *testing.T) {
	h := newHarness(t, 4)
	h.reconcile(t)

	h.setReplicas(t, 2)
	h.reconcile(t)

	want := []string{"scouts-0", "scouts-1"}
	if got := h.bots.Names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("bots are %v, want %v; scaling down must keep the lowest ordinals", got, want)
	}
}

func TestPoolScalesToZero(t *testing.T) {
	h := newHarness(t, 2)
	h.reconcile(t)

	h.setReplicas(t, 0)
	h.reconcile(t)

	if got := h.bots.Names(); len(got) != 0 {
		t.Fatalf("bots are %v, want none", got)
	}
}

func TestPoolRewritesBotsWhenTheTemplateChanges(t *testing.T) {
	h := newHarness(t, 1)
	h.reconcile(t)

	pool, err := h.pools.Get(context.Background(), namespace, poolName)
	if err != nil {
		t.Fatalf("get pool: %v", err)
	}
	pool.Spec.Template.Spec.Server.Host = "mcp.staging.svc"
	if _, err := h.pools.Update(context.Background(), pool); err != nil {
		t.Fatalf("update pool: %v", err)
	}
	h.reconcile(t)

	bot, err := h.bots.Get(context.Background(), namespace, "scouts-0")
	if err != nil {
		t.Fatalf("get bot: %v", err)
	}
	if bot.Spec.Server.Host != "mcp.staging.svc" {
		t.Fatalf("bot still points at %q", bot.Spec.Server.Host)
	}
}

func TestPoolStatusCountsLinkedBots(t *testing.T) {
	h := newHarness(t, 2)
	h.reconcile(t)

	bot, err := h.bots.Get(context.Background(), namespace, "scouts-0")
	if err != nil {
		t.Fatalf("get bot: %v", err)
	}
	bot.Status.Phase = v1alpha1.BotPhaseRunning
	bot.Status.Link = v1alpha1.LinkStateLinked
	if _, err := h.bots.Update(context.Background(), bot); err != nil {
		t.Fatalf("update bot: %v", err)
	}
	h.reconcile(t)

	pool, err := h.pools.Get(context.Background(), namespace, poolName)
	if err != nil {
		t.Fatalf("get pool: %v", err)
	}
	if pool.Status.Replicas != 2 {
		t.Errorf("status.replicas is %d, want 2", pool.Status.Replicas)
	}
	if pool.Status.LinkedReplicas != 1 {
		t.Errorf("status.linkedReplicas is %d, want 1", pool.Status.LinkedReplicas)
	}
	if pool.Status.Selector == "" {
		t.Error("status.selector must be set or kubectl scale cannot resolve the scale subresource")
	}
}
