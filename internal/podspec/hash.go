package podspec

import (
	corev1 "k8s.io/api/core/v1"

	"github.com/mc-agents/operator/internal/hash"
)

func Hash(spec *corev1.PodSpec) string {
	return hash.JSON(spec)
}
