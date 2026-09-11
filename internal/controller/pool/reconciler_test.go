package pool_test

import (
	"context"
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/mc-agents/operator/api/v1alpha1"
	poolctl "github.com/mc-agents/operator/internal/controller/pool"
)

const (
	namespace = "qa"
	poolName  = "scouts"
	poolUID   = types.UID("pool-uid")
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(v1alpha1.AddToScheme(scheme))
}

type harness struct {
	client client.Client
	under  *poolctl.Reconciler
}

func newHarness(t *testing.T, replicas int32) *harness {
	t.Helper()

	pool := &v1alpha1.MinecraftBotPool{
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
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.MinecraftBotPool{}, &v1alpha1.MinecraftBot{}).
		WithObjects(pool).
		Build()

	return &harness{client: c, under: poolctl.NewReconciler(c, record.NewFakeRecorder(32))}
}

func (h *harness) reconcile(t *testing.T) {
	t.Helper()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: poolName}}
	if _, err := h.under.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
}

func (h *harness) pool(t *testing.T) *v1alpha1.MinecraftBotPool {
	t.Helper()
	var pool v1alpha1.MinecraftBotPool
	key := types.NamespacedName{Namespace: namespace, Name: poolName}
	if err := h.client.Get(context.Background(), key, &pool); err != nil {
		t.Fatalf("get pool: %v", err)
	}
	return &pool
}

func (h *harness) bot(t *testing.T, name string) *v1alpha1.MinecraftBot {
	t.Helper()
	var bot v1alpha1.MinecraftBot
	key := types.NamespacedName{Namespace: namespace, Name: name}
	if err := h.client.Get(context.Background(), key, &bot); err != nil {
		t.Fatalf("get bot %s: %v", name, err)
	}
	return &bot
}

func (h *harness) botNames(t *testing.T) []string {
	t.Helper()
	var list v1alpha1.MinecraftBotList
	if err := h.client.List(context.Background(), &list, client.InNamespace(namespace)); err != nil {
		t.Fatalf("list bots: %v", err)
	}
	names := make([]string, 0, len(list.Items))
	for _, bot := range list.Items {
		names = append(names, bot.Name)
	}
	return names
}

func (h *harness) setReplicas(t *testing.T, replicas int32) {
	t.Helper()
	pool := h.pool(t)
	pool.Spec.Replicas = &replicas
	if err := h.client.Update(context.Background(), pool); err != nil {
		t.Fatalf("update pool: %v", err)
	}
}

func TestPoolCreatesOrdinalBots(t *testing.T) {
	h := newHarness(t, 3)
	h.reconcile(t)

	want := []string{"scouts-0", "scouts-1", "scouts-2"}
	if got := h.botNames(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("bots are %v, want %v", got, want)
	}

	bot := h.bot(t, "scouts-0")
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
	first := h.bot(t, "scouts-1")

	h.reconcile(t)
	if second := h.bot(t, "scouts-1"); first.UID != second.UID {
		t.Fatal("a second reconcile replaced a bot that was already correct")
	}
}

func TestPoolScalesDownFromTheTop(t *testing.T) {
	h := newHarness(t, 4)
	h.reconcile(t)

	h.setReplicas(t, 2)
	h.reconcile(t)

	want := []string{"scouts-0", "scouts-1"}
	if got := h.botNames(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("bots are %v, want %v; scaling down must keep the lowest ordinals", got, want)
	}
}

func TestPoolScalesToZero(t *testing.T) {
	h := newHarness(t, 2)
	h.reconcile(t)

	h.setReplicas(t, 0)
	h.reconcile(t)

	if got := h.botNames(t); len(got) != 0 {
		t.Fatalf("bots are %v, want none", got)
	}
}

func TestPoolRewritesBotsWhenTheTemplateChanges(t *testing.T) {
	h := newHarness(t, 1)
	h.reconcile(t)

	pool := h.pool(t)
	pool.Spec.Template.Spec.Server.Host = "mcp.staging.svc"
	if err := h.client.Update(context.Background(), pool); err != nil {
		t.Fatalf("update pool: %v", err)
	}
	h.reconcile(t)

	if host := h.bot(t, "scouts-0").Spec.Server.Host; host != "mcp.staging.svc" {
		t.Fatalf("bot still points at %q", host)
	}
}

func TestPoolStatusCountsLinkedBots(t *testing.T) {
	h := newHarness(t, 2)
	h.reconcile(t)

	bot := h.bot(t, "scouts-0")
	bot.Status.Phase = v1alpha1.BotPhaseRunning
	bot.Status.Link = v1alpha1.LinkStateLinked
	if err := h.client.Status().Update(context.Background(), bot); err != nil {
		t.Fatalf("update bot status: %v", err)
	}
	h.reconcile(t)

	pool := h.pool(t)
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
