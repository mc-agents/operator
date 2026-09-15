package mcpserver

import (
	"context"
	"strconv"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

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

func TestTheGeneratedTokenIsMadeOnceAndKept(t *testing.T) {
	h := newHarness(t, newServer())
	h.reconcile(t)
	h.reconcile(t)

	var secret corev1.Secret
	if err := h.get(t, "mc-agents-mcp-server-auth", &secret); err != nil {
		t.Fatalf("get secret: %v", err)
	}
	if h.tokens != 1 {
		t.Fatalf("generated %d tokens, want exactly one", h.tokens)
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

func TestAnExistingSecretIsUsedAndNoneIsMade(t *testing.T) {
	server := newServer()
	server.Spec.Auth.ExistingSecret = "shared-token"
	h := newHarness(t, server)
	h.reconcile(t)

	if err := h.get(t, "mc-agents-mcp-server-auth", &corev1.Secret{}); !apierrors.IsNotFound(err) {
		t.Fatalf("a generated secret exists alongside the existing one: %v", err)
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
		"MCP_BOTS_MCP_HOST":     "mc-agents-mcp-server.hyperfarm-local.svc",
		"MCP_BOTS_PROFILE_KIND": "MinecraftBotProfile",
		"MCP_BOTS_PROFILE_NAME": "quest",
		"MCP_MAX_BOTS":          "16",
	}
	for name, value := range want {
		if got := envVar(c, name).Value; got != value {
			t.Errorf("%s got %q, want %q", name, got, value)
		}
	}
	if c.Image != "junhyung.cloud/library/mcp-server:0.55.0" {
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
