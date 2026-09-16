package bot_test

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/mc-agents/operator/api/v1alpha1"
	"github.com/mc-agents/operator/internal/botimage"
	botctl "github.com/mc-agents/operator/internal/controller/bot"
	"github.com/mc-agents/operator/internal/podspec"
	"github.com/mc-agents/operator/internal/throttle"
)

const (
	namespace   = "qa"
	botName     = "scout"
	botUID      = types.UID("bot-uid")
	previousUID = types.UID("previous-bot-uid")
	stalePodUID = types.UID("stale-pod-uid")

	waitingMessage = "waiting for the previous pod to terminate"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(v1alpha1.AddToScheme(scheme))
}

type harness struct {
	client client.Client
	under  *botctl.Reconciler
}

func newHarness(t *testing.T, c client.Client, apiReader client.Reader) *harness {
	t.Helper()
	images := botimage.NewResolver("", botimage.Tags{Fabric: "0.2.0", Azalea: "0.2.0"})
	builder := podspec.NewBuilder(images, podspec.Defaults{})
	under := botctl.NewReconciler(c, apiReader, record.NewFakeRecorder(32), builder, throttle.NewIntervalGate(0))
	return &harness{client: c, under: under}
}

func newBot() *v1alpha1.MinecraftBot {
	return &v1alpha1.MinecraftBot{
		ObjectMeta: metav1.ObjectMeta{Name: botName, Namespace: namespace, UID: botUID, Generation: 1},
		Spec: v1alpha1.MinecraftBotSpec{
			Kind:             v1alpha1.BotKindAzalea,
			MinecraftVersion: "26.1.2",
			Server:           v1alpha1.MCPServerRef{Host: "mcp.qa.svc", Port: 8765},
		},
	}
}

func podOwnedBy(owner *metav1.OwnerReference) *corev1.Pod {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: botName, Namespace: namespace, UID: stalePodUID}}
	if owner != nil {
		pod.OwnerReferences = []metav1.OwnerReference{*owner}
	}
	return pod
}

func controllerRef(name string, uid types.UID) *metav1.OwnerReference {
	owner := &v1alpha1.MinecraftBot{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, UID: uid}}
	return metav1.NewControllerRef(owner, v1alpha1.SchemeGroupVersion.WithKind("MinecraftBot"))
}

// The fake client refuses a deletionTimestamp without a finalizer.
func terminating(pod *corev1.Pod) *corev1.Pod {
	now := metav1.Now()
	pod.DeletionTimestamp = &now
	pod.Finalizers = []string{"test/terminating"}
	return pod
}

func (h *harness) reconcile(t *testing.T) ctrl.Result {
	t.Helper()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: botName}}
	result, err := h.under.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return result
}

func (h *harness) bot(t *testing.T) *v1alpha1.MinecraftBot {
	t.Helper()
	var bot v1alpha1.MinecraftBot
	key := types.NamespacedName{Namespace: namespace, Name: botName}
	if err := h.client.Get(context.Background(), key, &bot); err != nil {
		t.Fatalf("get bot: %v", err)
	}
	return &bot
}

func (h *harness) pod(t *testing.T) (*corev1.Pod, bool) {
	t.Helper()
	var pod corev1.Pod
	key := types.NamespacedName{Namespace: namespace, Name: botName}
	err := h.client.Get(context.Background(), key, &pod)
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	if err != nil {
		t.Fatalf("get pod: %v", err)
	}
	return &pod, true
}

func (h *harness) expectWaiting(t *testing.T, result ctrl.Result) {
	t.Helper()
	if result.RequeueAfter != 2*time.Second {
		t.Errorf("requeue after %s, want 2s", result.RequeueAfter)
	}
	bot := h.bot(t)
	if bot.Status.Phase != v1alpha1.BotPhasePending {
		t.Errorf("phase is %q, want Pending", bot.Status.Phase)
	}
	if bot.Status.LastError != waitingMessage {
		t.Errorf("lastError is %q, want %q", bot.Status.LastError, waitingMessage)
	}
}

func (h *harness) expectOwnPod(t *testing.T) {
	t.Helper()
	pod, ok := h.pod(t)
	if !ok {
		t.Fatal("no pod was created once the previous one was gone")
	}
	if owner := metav1.GetControllerOf(pod); owner == nil || owner.UID != botUID {
		t.Fatalf("the new pod is owned by %+v, want this bot", owner)
	}
	if phase := h.bot(t).Status.Phase; phase != v1alpha1.BotPhaseStarting {
		t.Errorf("phase is %q, want Starting", phase)
	}
}

func TestAPredecessorsTerminatingPodIsWaitedFor(t *testing.T) {
	stale := terminating(podOwnedBy(controllerRef(botName, previousUID)))
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.MinecraftBot{}).
		WithObjects(newBot(), stale).
		Build()
	h := newHarness(t, c, c)

	h.expectWaiting(t, h.reconcile(t))
	pod, ok := h.pod(t)
	if !ok {
		t.Fatal("a pod that is already terminating must be left to finish")
	}

	pod.Finalizers = nil
	if err := c.Update(context.Background(), pod); err != nil {
		t.Fatalf("finish terminating: %v", err)
	}
	h.reconcile(t)
	h.expectOwnPod(t)
}

func TestAPredecessorsPodTheCollectorHasNotReachedIsDeleted(t *testing.T) {
	var deleted []client.DeleteOption
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.MinecraftBot{}).
		WithObjects(newBot(), podOwnedBy(controllerRef(botName, previousUID))).
		WithInterceptorFuncs(interceptor.Funcs{
			Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				deleted = opts
				return c.Delete(ctx, obj, opts...)
			},
		}).
		Build()
	h := newHarness(t, c, c)

	h.expectWaiting(t, h.reconcile(t))
	if _, ok := h.pod(t); ok {
		t.Fatal("the previous bot's pod is still there")
	}
	var options client.DeleteOptions
	options.ApplyOptions(deleted)
	if options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != stalePodUID {
		t.Fatalf("the delete is not pinned to the stale pod's UID: %+v", options.Preconditions)
	}

	h.reconcile(t)
	h.expectOwnPod(t)
}

func TestACreateThatMeetsThePredecessorWaitsToo(t *testing.T) {
	// The cache has no pod yet, the API server still has the old one: the create
	// says AlreadyExists and the re-read has to tell a predecessor from a clash.
	tracker := clienttesting.NewObjectTracker(scheme, serializer.NewCodecFactory(scheme).UniversalDecoder())
	cached := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjectTracker(tracker).
		WithStatusSubresource(&v1alpha1.MinecraftBot{}).
		WithObjects(newBot(), podOwnedBy(controllerRef(botName, previousUID))).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, isPod := obj.(*corev1.Pod); isPod {
					return apierrors.NewNotFound(corev1.Resource("pods"), key.Name)
				}
				return c.Get(ctx, key, obj, opts...)
			},
		}).
		Build()
	live := fake.NewClientBuilder().WithScheme(scheme).WithObjectTracker(tracker).Build()
	h := newHarness(t, cached, live)

	h.expectWaiting(t, h.reconcile(t))
}

func TestAForeignPodIsStillAClash(t *testing.T) {
	cases := []struct {
		name  string
		owner *metav1.OwnerReference
	}{
		{name: "no owner at all", owner: nil},
		{name: "a bot of another name", owner: controllerRef("other", previousUID)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := fake.NewClientBuilder().
				WithScheme(scheme).
				WithStatusSubresource(&v1alpha1.MinecraftBot{}).
				WithObjects(newBot(), podOwnedBy(tc.owner)).
				Build()
			h := newHarness(t, c, c)

			if result := h.reconcile(t); result.RequeueAfter != 0 {
				t.Errorf("a clash requeues after %s; the pod is nobody's to wait for", result.RequeueAfter)
			}
			bot := h.bot(t)
			if bot.Status.Phase != v1alpha1.BotPhaseFailed {
				t.Errorf("phase is %q, want Failed", bot.Status.Phase)
			}
			if !strings.Contains(bot.Status.LastError, "not owned by this bot") {
				t.Errorf("lastError %q does not call it a clash", bot.Status.LastError)
			}
			if _, ok := h.pod(t); !ok {
				t.Error("a foreign pod was deleted")
			}
		})
	}
}
