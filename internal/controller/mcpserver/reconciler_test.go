package mcpserver

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/managedfields"
	clientgoapplyconfigurations "k8s.io/client-go/applyconfigurations"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/structured-merge-diff/v6/typed"

	"github.com/mc-agents/operator/api/v1alpha1"
	"github.com/mc-agents/operator/internal/mcpserverspec"
)

const namespace = "hyperfarm-local"

type harness struct {
	client client.Client
	under  *Reconciler
	tokens int
}

func newHarness(t *testing.T, server *v1alpha1.MCPServer) *harness {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.MCPServer{}).
		WithTypeConverters(withoutNullStatus{clientgoapplyconfigurations.NewTypeConverter(scheme)}, managedfields.NewDeducedTypeConverter()).
		WithObjects(server).
		Build()
	h := &harness{client: c}
	h.under = NewReconciler(c, c, record.NewFakeRecorder(32), mcpserverspec.Defaults{Tag: "0.55.0"})
	h.under.token = func() (string, error) {
		h.tokens++
		return "token-" + strconv.Itoa(h.tokens), nil
	}
	return h
}

// The fake client still counts NetworkPolicy among the kinds with a status subresource, which
// networking.k8s.io/v1 dropped in 1.28, and stamps status: null onto every apply of one. The typed
// converter rejects the field it does not know, the fallback converter then types the other side
// under another schema, and the merge refuses the pair. Dropping the null before converting is what
// the API server would see.
type withoutNullStatus struct {
	managedfields.TypeConverter
}

func (c withoutNullStatus) ObjectToTyped(obj runtime.Object, opts ...typed.ValidationOptions) (*typed.TypedValue, error) {
	if u, ok := obj.(*unstructured.Unstructured); ok {
		if status, found := u.Object["status"]; found && status == nil {
			u = u.DeepCopy()
			delete(u.Object, "status")
			obj = u
		}
	}
	return c.TypeConverter.ObjectToTyped(obj, opts...)
}

func newServer() *v1alpha1.MCPServer {
	return &v1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "mc-agents", Namespace: namespace, UID: "server-uid", Generation: 1},
	}
}

func (h *harness) reconcile(t *testing.T) {
	t.Helper()
	req := ctrl.Request{NamespacedName: client.ObjectKey{Namespace: namespace, Name: "mc-agents"}}
	if _, err := h.under.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
}

func (h *harness) get(t *testing.T, name string, obj client.Object) error {
	t.Helper()
	return h.client.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, obj)
}

func (h *harness) server(t *testing.T, name string) v1alpha1.MCPServer {
	t.Helper()
	var server v1alpha1.MCPServer
	if err := h.get(t, name, &server); err != nil {
		t.Fatal(err)
	}
	return server
}

func TestTheGeneratedTokenIsMadeOnceAndKept(t *testing.T) {
	h := newHarness(t, newServer())
	h.reconcile(t)
	h.reconcile(t)

	var auth, link corev1.Secret
	if err := h.get(t, "mc-agents-mcp-server-auth", &auth); err != nil {
		t.Fatalf("get auth secret: %v", err)
	}
	if err := h.get(t, "mc-agents-mcp-server-link", &link); err != nil {
		t.Fatalf("get link secret: %v", err)
	}
	if h.tokens != 2 {
		t.Fatalf("generated %d tokens, want exactly one per secret", h.tokens)
	}
	// Two values, and never the same one: a bot that reads its link token must not hold the MCP port.
	if auth.StringData["token"] == link.StringData["token"] {
		t.Fatal("the link secret carries the auth token")
	}

	var server v1alpha1.MCPServer
	if err := h.get(t, "mc-agents", &server); err != nil {
		t.Fatal(err)
	}
	if server.Status.TokenSecretRef == nil || server.Status.TokenSecretRef.Name != "mc-agents-mcp-server-auth" {
		t.Fatalf("status token ref %+v", server.Status.TokenSecretRef)
	}
	if want := "http://mc-agents-mcp-server.hyperfarm-local.svc:3000/mcp"; server.Status.Endpoint != want {
		t.Fatalf("endpoint got %q, want %q", server.Status.Endpoint, want)
	}
}

func TestAServerPodTheKubeletCannotStartNamesTheReason(t *testing.T) {
	server := newServer()
	h := newHarness(t, server)
	h.reconcile(t)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mc-agents-mcp-server-7d9f8b6c5-x2k9q",
			Namespace: namespace,
			Labels:    mcpserverspec.Labels(server),
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: mcpserverspec.ContainerName,
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
					Reason:  "ImagePullBackOff",
					Message: `Back-off pulling image "junhyung.cloud/mc-agents/mcp-server:0.55.o"`,
				}},
			}},
		},
	}
	if err := h.client.Create(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	h.reconcile(t)

	var live v1alpha1.MCPServer
	if err := h.get(t, "mc-agents", &live); err != nil {
		t.Fatal(err)
	}
	ready := meta.FindStatusCondition(live.Status.Conditions, v1alpha1.ConditionReady)
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != "ImagePullBackOff" {
		t.Fatalf("Ready is %+v, want False/ImagePullBackOff", ready)
	}
	if live.Status.Phase != v1alpha1.MCPServerPhaseFailed {
		t.Fatalf("phase got %q, want Failed", live.Status.Phase)
	}
	if !strings.Contains(ready.Message, "0.55.o") {
		t.Fatalf("Ready message %q does not carry what the kubelet said", ready.Message)
	}
	if got := serverOf(context.Background(), pod); len(got) != 1 || got[0].Name != "mc-agents" {
		t.Fatalf("the pod maps to %v, want the MCPServer it runs", got)
	}
}

func TestTheBotPortStaysOnAClusterIPService(t *testing.T) {
	server := newServer()
	server.Spec.Service.Type = corev1.ServiceTypeLoadBalancer
	h := newHarness(t, server)
	h.reconcile(t)

	var mcp, bots corev1.Service
	if err := h.get(t, "mc-agents-mcp-server", &mcp); err != nil {
		t.Fatal(err)
	}
	if err := h.get(t, "mc-agents-mcp-server-bots", &bots); err != nil {
		t.Fatal(err)
	}
	if mcp.Spec.Type != corev1.ServiceTypeLoadBalancer || len(mcp.Spec.Ports) != 1 || mcp.Spec.Ports[0].Name != "mcp" {
		t.Errorf("the MCP Service is %s with ports %+v, want a LoadBalancer carrying mcp alone", mcp.Spec.Type, mcp.Spec.Ports)
	}
	if bots.Spec.Type != corev1.ServiceTypeClusterIP || len(bots.Spec.Ports) != 1 || bots.Spec.Ports[0].Name != "bot-link" {
		t.Errorf("the bots Service is %s with ports %+v, want a ClusterIP carrying bot-link alone", bots.Spec.Type, bots.Spec.Ports)
	}
	if err := h.get(t, "mc-agents-mcp-server", &networkingv1.NetworkPolicy{}); err != nil {
		t.Errorf("no NetworkPolicy in front of the bot port: %v", err)
	}
}

func TestAnExistingSecretIsUsedAndNoneIsMade(t *testing.T) {
	server := newServer()
	server.Spec.Auth.ExistingSecret = "shared-token"
	h := newHarness(t, server)
	h.reconcile(t)

	if err := h.get(t, "mc-agents-mcp-server-auth", &corev1.Secret{}); !apierrors.IsNotFound(err) {
		t.Fatalf("a generated secret exists alongside the existing one: %v", err)
	}
	// The link token is the operator's whichever way the auth token comes: nothing in the spec
	// supplies one, and the bots are told where it is by the server.
	if err := h.get(t, "mc-agents-mcp-server-link", &corev1.Secret{}); err != nil {
		t.Fatalf("get link secret: %v", err)
	}
	var deployment appsv1.Deployment
	if err := h.get(t, "mc-agents-mcp-server", &deployment); err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	ref := envVar(deployment.Spec.Template.Spec.Containers[0], "MCP_AUTH_TOKEN").ValueFrom.SecretKeyRef
	if ref.Name != "shared-token" || ref.Key != "token" {
		t.Fatalf("token env points at %s/%s", ref.Name, ref.Key)
	}
}

func TestTurningProvisioningOffTakesTheRoleBack(t *testing.T) {
	h := newHarness(t, newServer())
	h.reconcile(t)
	if err := h.get(t, "mc-agents-mcp-server", &rbacv1.Role{}); err != nil {
		t.Fatalf("role missing while provisioning is on: %v", err)
	}

	var server v1alpha1.MCPServer
	if err := h.get(t, "mc-agents", &server); err != nil {
		t.Fatal(err)
	}
	off := false
	server.Spec.Bots.Provision = &off
	if err := h.client.Update(context.Background(), &server); err != nil {
		t.Fatal(err)
	}
	h.reconcile(t)

	if err := h.get(t, "mc-agents-mcp-server", &rbacv1.Role{}); !apierrors.IsNotFound(err) {
		t.Fatalf("role survived provisioning being turned off: %v", err)
	}
	if err := h.get(t, "mc-agents-mcp-server", &rbacv1.RoleBinding{}); !apierrors.IsNotFound(err) {
		t.Fatalf("rolebinding survived provisioning being turned off: %v", err)
	}
	var deployment appsv1.Deployment
	if err := h.get(t, "mc-agents-mcp-server", &deployment); err != nil {
		t.Fatal(err)
	}
	if got := envVar(deployment.Spec.Template.Spec.Containers[0], "MCP_BOTS_PROVISION").Value; got != "never" {
		t.Fatalf("MCP_BOTS_PROVISION got %q, want never", got)
	}
}

func TestTheServerMakesBotsInItsOwnNamespaceWithItsProfile(t *testing.T) {
	server := newServer()
	server.Spec.Bots.ProfileRef = &v1alpha1.ProfileRef{Name: "quest"}
	h := newHarness(t, server)
	h.reconcile(t)

	var deployment appsv1.Deployment
	if err := h.get(t, "mc-agents-mcp-server", &deployment); err != nil {
		t.Fatal(err)
	}
	c := deployment.Spec.Template.Spec.Containers[0]
	want := map[string]string{
		"MCP_BOTS_NAMESPACE":    namespace,
		"MCP_BOTS_MCP_HOST":     "mc-agents-mcp-server-bots.hyperfarm-local.svc",
		"MCP_BIND_HOST":         "0.0.0.0",
		"MCP_BOTS_PROFILE_KIND": "MinecraftBotProfile",
		"MCP_BOTS_PROFILE_NAME": "quest",
		"MCP_MAX_BOTS":          "16",
	}
	for name, value := range want {
		if got := envVar(c, name).Value; got != value {
			t.Errorf("%s got %q, want %q", name, got, value)
		}
	}
	if c.Image != "junhyung.cloud/mc-agents/mcp-server:0.55.0" {
		t.Errorf("image got %q", c.Image)
	}
	if owner := metav1.GetControllerOf(&deployment); owner == nil || owner.UID != "server-uid" {
		t.Errorf("deployment is not owned by the MCPServer: %+v", owner)
	}
}

func envVar(c corev1.Container, name string) corev1.EnvVar {
	for _, e := range c.Env {
		if e.Name == name {
			return e
		}
	}
	return corev1.EnvVar{}
}

// One row per state the server can be in, with the inputs that put it there, and the cross-check
// that is the reason for doing both in one table: Running and Ready=True are one moment. A branch
// that moves the phase without the condition, or the condition without the phase, fails here
// rather than reaching a cluster as an MCPServer printing Running next to Ready False.
func TestEveryPhaseIsReachedAndSaysWhatReadyDoes(t *testing.T) {
	blocked := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "mc-agents-mcp-server-7d9f8b6c5-x2k9q", Namespace: namespace},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:  mcpserverspec.ContainerName,
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "no such tag"}},
			}},
		},
	}
	now := metav1.Now()
	draining := *blocked.DeepCopy()
	draining.DeletionTimestamp = &now

	cases := []struct {
		name       string
		deployment *appsv1.Deployment
		pods       []corev1.Pod
		phase      v1alpha1.MCPServerPhase
		reason     string
	}{{
		name:   "the Deployment is not in view yet",
		phase:  v1alpha1.MCPServerPhasePending,
		reason: "Progressing",
	}, {
		name:       "the Deployment is there and its pod is coming up",
		deployment: deployment(1, 1, 0),
		phase:      v1alpha1.MCPServerPhaseStarting,
		reason:     "Progressing",
	}, {
		// Recreate: the pod that is available belongs to the spec before this one, so the server
		// an agent would reach is not the server the spec asks for.
		name:       "a new generation the Deployment controller has not acted on",
		deployment: deployment(2, 1, 1),
		phase:      v1alpha1.MCPServerPhaseStarting,
		reason:     "Progressing",
	}, {
		name:       "a pod is available",
		deployment: deployment(1, 1, 1),
		phase:      v1alpha1.MCPServerPhaseRunning,
		reason:     "Available",
	}, {
		name:       "the kubelet cannot bring the pod up",
		deployment: deployment(1, 1, 0),
		pods:       []corev1.Pod{blocked},
		phase:      v1alpha1.MCPServerPhaseFailed,
		reason:     "ImagePullBackOff",
	}, {
		name:       "the rollout has stalled",
		deployment: stalled(deployment(1, 1, 0)),
		phase:      v1alpha1.MCPServerPhaseFailed,
		reason:     "ProgressDeadlineExceeded",
	}, {
		// The Recreate strategy takes the old pod down before the new one goes up, and that pod
		// crash-looping on its way out is not what the new spec is doing.
		name:       "the pod on its way out is blocked",
		deployment: deployment(2, 1, 0),
		pods:       []corev1.Pod{draining},
		phase:      v1alpha1.MCPServerPhaseStarting,
		reason:     "Progressing",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := observe(newServer(), tc.deployment, tc.pods)
			if got.phase != tc.phase {
				t.Errorf("phase got %q, want %q", got.phase, tc.phase)
			}
			if reason := got.ready(1).Reason; reason != tc.reason {
				t.Errorf("Ready reason got %q, want %q", reason, tc.reason)
			}
		})
	}
}

// The one place the two answers are tied, checked where the tie is made rather than at every call
// site that could forget it: Running is the server accepting bots, and that is the only phase a
// reader waiting on Ready should be let through by.
func TestOnlyRunningReadsAsReady(t *testing.T) {
	for _, phase := range []v1alpha1.MCPServerPhase{
		v1alpha1.MCPServerPhasePending,
		v1alpha1.MCPServerPhaseStarting,
		v1alpha1.MCPServerPhaseRunning,
		v1alpha1.MCPServerPhaseFailed,
	} {
		ready := observation{phase: phase, reason: "Reason", message: "message"}.ready(7)
		want := metav1.ConditionFalse

		if phase == v1alpha1.MCPServerPhaseRunning {
			want = metav1.ConditionTrue
		}
		if ready.Status != want {
			t.Errorf("phase %q reads as Ready %q, want %q", phase, ready.Status, want)
		}
		if ready.ObservedGeneration != 7 || ready.Reason != "Reason" || ready.Message != "message" {
			t.Errorf("phase %q lost what it was built with: %+v", phase, ready)
		}
	}
}

// What observe worked out has to survive the trip into status, and Ready has to land beside it:
// the two are written in one call, and a status that carries only one of them is the disagreement
// the phase exists to avoid.
func TestThePhaseFollowsTheDeploymentIntoStatus(t *testing.T) {
	h := newHarness(t, newServer())
	h.reconcile(t)

	server := h.server(t, "mc-agents")
	if server.Status.Phase != v1alpha1.MCPServerPhaseStarting {
		t.Fatalf("phase after the first reconcile got %q, want Starting", server.Status.Phase)
	}

	var deployment appsv1.Deployment
	if err := h.get(t, "mc-agents-mcp-server", &deployment); err != nil {
		t.Fatal(err)
	}
	deployment.Status = appsv1.DeploymentStatus{ObservedGeneration: deployment.Generation, AvailableReplicas: 1}
	if err := h.client.Status().Update(context.Background(), &deployment); err != nil {
		t.Fatal(err)
	}
	h.reconcile(t)

	server = h.server(t, "mc-agents")
	if server.Status.Phase != v1alpha1.MCPServerPhaseRunning {
		t.Fatalf("phase with an available pod got %q, want Running", server.Status.Phase)
	}
	ready := meta.FindStatusCondition(server.Status.Conditions, v1alpha1.ConditionReady)
	if ready == nil || ready.Status != metav1.ConditionTrue {
		t.Fatalf("Ready is %+v beside a Running phase", ready)
	}
}

// Failed is also what the operator's own failures read as: a server whose token cannot be minted
// has no pod coming and never will, and Pending would leave it looking like one more reconcile
// would fix it.
func TestAServerWhoseTokenCannotBeMintedIsFailed(t *testing.T) {
	h := newHarness(t, newServer())
	h.under.token = func() (string, error) { return "", errors.New("no entropy") }

	req := ctrl.Request{NamespacedName: client.ObjectKey{Namespace: namespace, Name: "mc-agents"}}
	if _, err := h.under.Reconcile(context.Background(), req); err == nil {
		t.Fatal("a token that cannot be generated reconciled without error")
	}

	server := h.server(t, "mc-agents")
	if server.Status.Phase != v1alpha1.MCPServerPhaseFailed {
		t.Fatalf("phase got %q, want Failed", server.Status.Phase)
	}
	ready := meta.FindStatusCondition(server.Status.Conditions, v1alpha1.ConditionReady)
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != "TokenFailed" {
		t.Fatalf("Ready is %+v, want False/TokenFailed beside the Failed phase", ready)
	}
}

func deployment(generation, observed int64, available int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Generation: generation},
		Status:     appsv1.DeploymentStatus{ObservedGeneration: observed, AvailableReplicas: available},
	}
}

func stalled(d *appsv1.Deployment) *appsv1.Deployment {
	d.Status.Conditions = []appsv1.DeploymentCondition{{
		Type:    appsv1.DeploymentProgressing,
		Status:  corev1.ConditionFalse,
		Reason:  "ProgressDeadlineExceeded",
		Message: `ReplicaSet "mc-agents-mcp-server-7d9f8b6c5" has timed out progressing`,
	}}
	return d
}
