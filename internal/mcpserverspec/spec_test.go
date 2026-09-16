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
	if from.NamespaceSelector == nil || len(from.NamespaceSelector.MatchLabels) != 0 {
		t.Errorf("bots dial in from any namespace, the peer selects %+v", from.NamespaceSelector)
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

func envVar(deployment *appsv1.Deployment, name string) *corev1.EnvVar {
	for _, e := range deployment.Spec.Template.Spec.Containers[0].Env {
		if e.Name == name {
			return &e
		}
	}
	return nil
}
