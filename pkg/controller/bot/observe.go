package bot

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"

	"github.com/mc-agents/operator/api/v1alpha1"
)

type Observation struct {
	Phase   v1alpha1.BotPhase
	Link    v1alpha1.LinkState
	Message string
}

var terminalWaitReasons = map[string]bool{
	"ImagePullBackOff":           true,
	"ErrImagePull":               true,
	"InvalidImageName":           true,
	"CreateContainerConfigError": true,
	"CreateContainerError":       true,
	"CrashLoopBackOff":           true,
}

func Observe(pod *corev1.Pod) Observation {
	if pod == nil {
		return Observation{Phase: v1alpha1.BotPhasePending, Link: v1alpha1.LinkStateUnknown}
	}
	if pod.DeletionTimestamp != nil {
		return Observation{Phase: v1alpha1.BotPhaseTerminating, Link: v1alpha1.LinkStateUnknown}
	}

	link := linkState(pod)

	if reason, message, ok := blockedContainer(pod); ok {
		return Observation{
			Phase:   v1alpha1.BotPhaseFailed,
			Link:    link,
			Message: fmt.Sprintf("%s: %s", reason, message),
		}
	}

	switch pod.Status.Phase {
	case corev1.PodRunning:
		return Observation{Phase: v1alpha1.BotPhaseRunning, Link: link}
	case corev1.PodSucceeded:
		return Observation{
			Phase:   v1alpha1.BotPhaseFailed,
			Link:    v1alpha1.LinkStateLost,
			Message: "bot process exited",
		}
	case corev1.PodFailed:
		return Observation{
			Phase:   v1alpha1.BotPhaseFailed,
			Link:    v1alpha1.LinkStateLost,
			Message: podFailureMessage(pod),
		}
	default:
		return Observation{Phase: v1alpha1.BotPhaseStarting, Link: link}
	}
}

func linkState(pod *corev1.Pod) v1alpha1.LinkState {
	if podReady(pod) {
		return v1alpha1.LinkStateLinked
	}
	if pod.Status.Phase != corev1.PodRunning {
		return v1alpha1.LinkStateUnknown
	}
	for _, status := range pod.Status.ContainerStatuses {
		if status.RestartCount > 0 || status.LastTerminationState.Terminated != nil {
			return v1alpha1.LinkStateLost
		}
	}
	return v1alpha1.LinkStateWaiting
}

func podReady(pod *corev1.Pod) bool {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}

func blockedContainer(pod *corev1.Pod) (string, string, bool) {
	all := make([]corev1.ContainerStatus, 0, len(pod.Status.InitContainerStatuses)+len(pod.Status.ContainerStatuses))
	all = append(all, pod.Status.InitContainerStatuses...)
	all = append(all, pod.Status.ContainerStatuses...)
	for _, status := range all {
		waiting := status.State.Waiting
		if waiting == nil || !terminalWaitReasons[waiting.Reason] {
			continue
		}
		message := waiting.Message
		if message == "" {
			message = status.Name
		}
		return waiting.Reason, message, true
	}
	return "", "", false
}

func podFailureMessage(pod *corev1.Pod) string {
	if pod.Status.Message != "" {
		return pod.Status.Message
	}
	for _, status := range pod.Status.ContainerStatuses {
		if t := status.State.Terminated; t != nil && t.ExitCode != 0 {
			return fmt.Sprintf("%s exited with code %d (%s)", status.Name, t.ExitCode, t.Reason)
		}
	}
	return "pod failed"
}
