package v1alpha1_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mc-agents/operator/api/v1alpha1"
)

func TestPoolStatusCountersSurviveZero(t *testing.T) {
	encoded, err := json.Marshal(v1alpha1.MinecraftBotPoolStatus{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, field := range []string{`"replicas":0`, `"readyReplicas":0`, `"linkedReplicas":0`} {
		if !strings.Contains(string(encoded), field) {
			t.Errorf("%s is missing from %s; kubectl scale resolves .status.replicas and a pool at zero would lose it", field, encoded)
		}
	}
}
