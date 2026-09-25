package mcpserverspec_test

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mc-agents/operator/api/v1alpha1"
	"github.com/mc-agents/operator/internal/mcpserverspec"
)

func newServer() *v1alpha1.MCPServer {
	return &v1alpha1.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: "mc-agents", Namespace: "game", UID: "server-uid"}}
}

func TestTheNetworkPolicyGatesTheBotPortAndLeavesTheMCPPortToTheToken(t *testing.T) {
	policy := mcpserverspec.NetworkPolicy(newServer())

	if len(policy.Spec.Ingress) != 2 {
		t.Fatalf("want one rule per port, got %d", len(policy.Spec.Ingress))
	}
	bots := policy.Spec.Ingress[0]
	if bots.Ports[0].Port.String() != "bot-link" || len(bots.From) != 1 {
		t.Fatalf("the first rule is not the bot-link gate: %+v", bots)
	}
	from := bots.From[0]
	if from.NamespaceSelector == nil || from.NamespaceSelector.MatchLabels[corev1.LabelMetadataName] != "game" {
		t.Errorf("only the server's own namespace may dial the bot port, the peer selects %+v", from.NamespaceSelector)
	}
	if from.PodSelector == nil || from.PodSelector.MatchLabels[v1alpha1.LabelName] != v1alpha1.BotPodName {
		t.Errorf("only bot pods may dial the bot port, the peer selects %+v", from.PodSelector)
	}
	mcp := policy.Spec.Ingress[1]
	if mcp.Ports[0].Port.String() != "mcp" || len(mcp.From) != 0 {
		t.Errorf("the MCP port is gated by its token, not by peers: %+v", mcp)
	}
}

func TestTheServerPodIsLabelledForTheCacheWithoutMovingTheSelector(t *testing.T) {
	deployment := mcpserverspec.Deployment(newServer(), mcpserverspec.Defaults{})

	if got := deployment.Spec.Template.Labels[v1alpha1.LabelManagedBy]; got != v1alpha1.ManagedByValue {
		t.Errorf("pod template managed-by is %q; the filtered cache would not hold the pod", got)
	}
	if _, moved := deployment.Spec.Selector.MatchLabels[v1alpha1.LabelManagedBy]; moved {
		t.Error("the selector is immutable on a Deployment and must stay the two labels it was")
	}
}

func TestTheServerIsHandedTheLinkTokenAndTheSecretItLivesIn(t *testing.T) {
	deployment := mcpserverspec.Deployment(newServer(), mcpserverspec.Defaults{})

	token := envVar(deployment, "BOT_LINK_TOKEN")
	if token == nil || token.ValueFrom == nil || token.ValueFrom.SecretKeyRef == nil {
		t.Fatalf("BOT_LINK_TOKEN is %+v, want a secretKeyRef", token)
	}
	if ref := token.ValueFrom.SecretKeyRef; ref.Name != "mc-agents-mcp-server-link" || ref.Key != "token" {
		t.Errorf("BOT_LINK_TOKEN comes from %s/%s, want mc-agents-mcp-server-link/token", ref.Name, ref.Key)
	}
	// The name, not the value: join-server writes it into every bot it creates as the ref the
	// operator turns into the same env on the bot's side.
	if name := envVar(deployment, "MCP_BOTS_LINK_SECRET"); name == nil || name.Value != "mc-agents-mcp-server-link" {
		t.Errorf("MCP_BOTS_LINK_SECRET is %+v, want the link Secret's name", name)
	}
}

func TestTheLogFormatReachesTheContainerOnlyWhenSet(t *testing.T) {
	server := newServer()
	if env := envVar(mcpserverspec.Deployment(server, mcpserverspec.Defaults{}), "LOGGING_STRUCTURED_FORMAT_CONSOLE"); env != nil {
		t.Fatalf("an empty logFormat still set %+v; the image's plain console lines are the default", env)
	}
	server.Spec.LogFormat = "ecs"
	env := envVar(mcpserverspec.Deployment(server, mcpserverspec.Defaults{}), "LOGGING_STRUCTURED_FORMAT_CONSOLE")
	if env == nil || env.Value != "ecs" {
		t.Fatalf("LOGGING_STRUCTURED_FORMAT_CONSOLE is %+v, want ecs", env)
	}
}

// The profile is how every bot join-server creates gets its image and tag. An empty
// MCP_BOTS_PROFILE_NAME is not the same as an absent one on the reader's side: it would name a
// profile called "" and fail every join instead of falling back to the default profile.
func TestTheProfileTheServerGivesItsBotsReachesTheContainerOnlyWhenNamed(t *testing.T) {
	server := newServer()
	for _, name := range []string{"MCP_BOTS_PROFILE_KIND", "MCP_BOTS_PROFILE_NAME"} {
		if env := envVar(mcpserverspec.Deployment(server, mcpserverspec.Defaults{}), name); env != nil {
			t.Fatalf("no profileRef still set %+v; the variable has to be absent, not empty", env)
		}
	}

	server.Spec.Bots.ProfileRef = &v1alpha1.ProfileRef{Name: "slow"}
	deployment := mcpserverspec.Deployment(server, mcpserverspec.Defaults{})
	// A ref written without a kind is a namespaced profile; the CRD default says so and nothing
	// re-reads it here.
	if kind := envVar(deployment, "MCP_BOTS_PROFILE_KIND"); kind == nil || kind.Value != string(v1alpha1.ProfileKindNamespaced) {
		t.Errorf("MCP_BOTS_PROFILE_KIND is %+v, want %s", kind, v1alpha1.ProfileKindNamespaced)
	}
	if name := envVar(deployment, "MCP_BOTS_PROFILE_NAME"); name == nil || name.Value != "slow" {
		t.Errorf("MCP_BOTS_PROFILE_NAME is %+v, want slow", name)
	}

	server.Spec.Bots.ProfileRef.Kind = v1alpha1.ProfileKindCluster
	kind := envVar(mcpserverspec.Deployment(server, mcpserverspec.Defaults{}), "MCP_BOTS_PROFILE_KIND")
	if kind == nil || kind.Value != string(v1alpha1.ProfileKindCluster) {
		t.Errorf("MCP_BOTS_PROFILE_KIND is %+v, want %s; a cluster profile read as a namespaced one is not there", kind, v1alpha1.ProfileKindCluster)
	}
}

func envVar(deployment *appsv1.Deployment, name string) *corev1.EnvVar {
	for _, e := range deployment.Spec.Template.Spec.Containers[0].Env {
		if e.Name == name {
			return &e
		}
	}
	return nil
}
