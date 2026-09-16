package bot_test

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
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
	"github.com/mc-agents/operator/internal/profile"
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
	client   client.Client
	under    *botctl.Reconciler
	recorder *record.FakeRecorder
}

func newHarness(t *testing.T, c client.Client, apiReader client.Reader) *harness {
	t.Helper()
	return newThrottledHarness(t, c, apiReader, throttle.NewIntervalGate(0))
}

func newThrottledHarness(t *testing.T, c client.Client, apiReader client.Reader, gate throttle.Gate) *harness {
	t.Helper()
	recorder := record.NewFakeRecorder(32)
	under := botctl.NewReconciler(c, apiReader, recorder, newBuilder(), gate)
	return &harness{client: c, under: under, recorder: recorder}
}

func newBuilder() podspec.Builder {
	images := botimage.NewResolver("", botimage.Tags{Fabric: "0.2.0", Azalea: "0.2.0"})
	return podspec.NewBuilder(images, podspec.Defaults{})
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

// ownPod is the pod the reconciler would build for newBot, spec hash included, so a test can
// start from a pod that matches and then move one thing.
func ownPod(t *testing.T) *corev1.Pod {
	t.Helper()
	pod, err := newBuilder().Build(newBot(), profile.Resolved{})
	if err != nil {
		t.Fatalf("build pod: %v", err)
	}
	pod.UID = stalePodUID
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

// events drains what the reconciler recorded so far, as "<type> <reason> <message>" lines.
func (h *harness) events() []string {
	var out []string
	for {
		select {
		case e := <-h.recorder.Events:
			out = append(out, e)
		default:
			return out
		}
	}
}

func hasEvent(events []string, prefix string) bool {
	for _, e := range events {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}

func withDeleteCapture(deleted *[]client.DeleteOption) interceptor.Funcs {
	return interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			*deleted = opts
			return c.Delete(ctx, obj, opts...)
		},
	}
}

func expectPinnedDelete(t *testing.T, deleted []client.DeleteOption) {
	t.Helper()
	var options client.DeleteOptions
	options.ApplyOptions(deleted)
	if options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != stalePodUID {
		t.Fatalf("the delete is not pinned to the pod's UID: %+v", options.Preconditions)
	}
}

func TestAPodThatNoLongerMatchesTheSpecIsReplaced(t *testing.T) {
	stale := ownPod(t)
	stale.Annotations[v1alpha1.AnnotationSpecHash] = "before-the-edit"
	var deleted []client.DeleteOption
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.MinecraftBot{}).
		WithObjects(newBot(), stale).
		WithInterceptorFuncs(withDeleteCapture(&deleted)).
		Build()
	h := newHarness(t, c, c)

	if result := h.reconcile(t); result.RequeueAfter != time.Second {
		t.Errorf("requeue after %s, want 1s for the replacement", result.RequeueAfter)
	}
	if _, ok := h.pod(t); ok {
		t.Fatal("the pod that no longer matches the spec is still there")
	}
	expectPinnedDelete(t, deleted)
	events := h.events()
	if !hasEvent(events, "Normal Recreating") {
		t.Errorf("no Recreating event in %q", events)
	}
	if hasEvent(events, "Warning PodExited") {
		t.Errorf("a spec change was blamed on the pod exiting: %q", events)
	}
}

func TestAPodThatExitedIsReplacedAndTheExitIsRecorded(t *testing.T) {
	exited := ownPod(t)
	exited.Status = corev1.PodStatus{
		Phase: corev1.PodFailed,
		ContainerStatuses: []corev1.ContainerStatus{{
			Name:  podspec.ContainerName,
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 137, Reason: "OOMKilled"}},
		}},
	}
	var deleted []client.DeleteOption
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.MinecraftBot{}).
		WithObjects(newBot(), exited).
		WithInterceptorFuncs(withDeleteCapture(&deleted)).
		Build()
	h := newHarness(t, c, c)

	if result := h.reconcile(t); result.RequeueAfter != time.Second {
		t.Errorf("requeue after %s, want 1s for the replacement", result.RequeueAfter)
	}
	if _, ok := h.pod(t); ok {
		t.Fatal("the exited pod is still there")
	}
	expectPinnedDelete(t, deleted)
	events := h.events()
	if !hasEvent(events, "Warning PodExited pod scout: bot exited with code 137 (OOMKilled)") {
		t.Errorf("the exit is not in the events as the pod reported it: %q", events)
	}
	if hasEvent(events, "Normal Recreating") {
		t.Errorf("an exit was blamed on the spec: %q", events)
	}
}

func TestAMatchingPodIsMirroredIntoStatus(t *testing.T) {
	pod := ownPod(t)
	pod.Status = running(true, 0).Status
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.MinecraftBot{}).
		WithObjects(newBot(), pod).
		Build()
	h := newHarness(t, c, c)

	if result := h.reconcile(t); result.RequeueAfter != 0 {
		t.Errorf("a pod that matches requeues after %s", result.RequeueAfter)
	}
	if live, ok := h.pod(t); !ok || live.UID != stalePodUID {
		t.Fatal("a pod that matches the spec was replaced")
	}
	bot := h.bot(t)
	if bot.Status.Phase != v1alpha1.BotPhaseRunning || bot.Status.Link != v1alpha1.LinkStateLinked {
		t.Errorf("status is %s/%s, want Running/Linked", bot.Status.Phase, bot.Status.Link)
	}
	ready := meta.FindStatusCondition(bot.Status.Conditions, v1alpha1.ConditionReady)
	if ready == nil || ready.Status != metav1.ConditionTrue || ready.Reason != v1alpha1.ReasonLinked {
		t.Errorf("Ready is %+v, want True/Linked", ready)
	}
	if !hasEvent(h.events(), "Normal Linked") {
		t.Error("linking left no event")
	}
}

func TestTheLinkTimelineIsRecorded(t *testing.T) {
	pod := ownPod(t)
	pod.Status = running(true, 0).Status
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.MinecraftBot{}).
		WithObjects(newBot(), pod).
		Build()
	h := newHarness(t, c, c)
	h.reconcile(t)
	h.events()

	lost, _ := h.pod(t)
	lost.Status = running(false, 1).Status
	if err := c.Status().Update(context.Background(), lost); err != nil {
		t.Fatalf("update pod status: %v", err)
	}
	h.reconcile(t)
	if events := h.events(); !hasEvent(events, "Warning LinkLost") {
		t.Errorf("losing the link left no event: %q", events)
	}
	h.reconcile(t)
	if events := h.events(); len(events) != 0 {
		t.Errorf("a reconcile that changed nothing recorded %q", events)
	}

	blocked, _ := h.pod(t)
	blocked.Status.ContainerStatuses[0].RestartCount = 3
	blocked.Status.ContainerStatuses[0].State = corev1.ContainerState{
		Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff", Message: "back-off 40s"},
	}
	if err := c.Status().Update(context.Background(), blocked); err != nil {
		t.Fatalf("update pod status: %v", err)
	}
	h.reconcile(t)
	if events := h.events(); !hasEvent(events, "Warning PodFailed pod scout: CrashLoopBackOff") {
		t.Errorf("the pod turning Failed left no event: %q", events)
	}
	degraded := meta.FindStatusCondition(h.bot(t).Status.Conditions, v1alpha1.ConditionDegraded)
	if degraded == nil || degraded.Status != metav1.ConditionTrue || degraded.Reason != "CrashLoopBackOff" {
		t.Errorf("Degraded is %+v, want True with the kubelet's reason", degraded)
	}
}

func TestDegradedNamesTheCauseWhenThereIsNoPod(t *testing.T) {
	bot := newBot()
	bot.Spec.ProfileRef = &v1alpha1.ProfileRef{Name: "missing"}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.MinecraftBot{}).
		WithObjects(bot).
		Build()
	h := newHarness(t, c, c)
	h.reconcile(t)

	degraded := meta.FindStatusCondition(h.bot(t).Status.Conditions, v1alpha1.ConditionDegraded)
	if degraded == nil || degraded.Status != metav1.ConditionTrue || degraded.Reason != v1alpha1.ReasonInvalidSpec {
		t.Fatalf("Degraded is %+v, want True/InvalidSpec; there is no pod to have failed", degraded)
	}
}

func TestThrottledIsRecordedOnceForTheWholeWait(t *testing.T) {
	gate := throttle.NewIntervalGate(time.Hour)
	gate.Acquire(time.Now())
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.MinecraftBot{}).
		WithObjects(newBot()).
		Build()
	h := newThrottledHarness(t, c, c, gate)

	if result := h.reconcile(t); result.RequeueAfter <= 0 {
		t.Fatalf("a throttled bot must be requeued for the wait, got %s", result.RequeueAfter)
	}
	if events := h.events(); !hasEvent(events, "Normal Throttled waiting for the spawn interval") {
		t.Fatalf("entering the wait left no event: %q", events)
	}
	if phase := h.bot(t).Status.Phase; phase != v1alpha1.BotPhasePending {
		t.Fatalf("phase is %q, want Pending", phase)
	}
	h.reconcile(t)
	h.reconcile(t)
	if events := h.events(); len(events) != 0 {
		t.Fatalf("every requeue in the same wait recorded again: %q", events)
	}
}

func TestAStatusWriteRetriesAConflict(t *testing.T) {
	conflicts := 1
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.MinecraftBot{}).
		WithObjects(newBot()).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				if conflicts > 0 {
					conflicts--
					return apierrors.NewConflict(v1alpha1.Resource("minecraftbots"), obj.GetName(), nil)
				}
				return c.Status().Update(ctx, obj, opts...)
			},
		}).
		Build()
	h := newHarness(t, c, c)
	h.reconcile(t)

	if phase := h.bot(t).Status.Phase; phase != v1alpha1.BotPhaseStarting {
		t.Fatalf("phase is %q after a conflict was retried, want Starting", phase)
	}
}

func TestABotDeletedUnderTheReconcileIsNotAnError(t *testing.T) {
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.MinecraftBot{}).
		WithObjects(newBot()).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				if err := c.Delete(ctx, obj); err != nil {
					return err
				}
				return c.Status().Update(ctx, obj, opts...)
			},
		}).
		Build()
	h := newHarness(t, c, c)

	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: botName}}
	if _, err := h.under.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("a bot deleted before its status write is a reconcile that has nothing left to do, got: %v", err)
	}
}
