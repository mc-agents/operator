package bot

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"

	"github.com/mc-agents/operator/api/v1alpha1"
	"github.com/mc-agents/operator/internal/podstatus"
)

type Observation struct {
	Phase   v1alpha1.BotPhase
	Link    v1alpha1.LinkState
	Reason  string
	Message string
}

func Observe(pod *corev1.Pod) Observation {
	if pod == nil {
		return Observation{Phase: v1alpha1.BotPhasePending, Link: v1alpha1.LinkStateUnknown, Reason: v1alpha1.ReasonPodPending}
	}
	if pod.DeletionTimestamp != nil {
		return Observation{Phase: v1alpha1.BotPhaseTerminating, Link: v1alpha1.LinkStateUnknown, Reason: v1alpha1.ReasonTerminating}
	}

	link := linkState(pod)

	if reason, message, ok := podstatus.Blocked(pod); ok {
		return Observation{
			Phase:   v1alpha1.BotPhaseFailed,
			Link:    link,
			Reason:  reason,
			Message: fmt.Sprintf("%s: %s", reason, message),
		}
	}

	switch pod.Status.Phase {
	case corev1.PodRunning:
		reason := v1alpha1.ReasonWaitingForLink
		if link == v1alpha1.LinkStateLinked {
			reason = v1alpha1.ReasonLinked
		}
		return Observation{Phase: v1alpha1.BotPhaseRunning, Link: link, Reason: reason}
	case corev1.PodSucceeded, corev1.PodFailed:
		return Observation{
			Phase:   v1alpha1.BotPhaseFailed,
			Link:    v1alpha1.LinkStateLost,
			Reason:  v1alpha1.ReasonPodExited,
			Message: exitMessage(pod),
		}
	default:
		observed := Observation{Phase: v1alpha1.BotPhaseStarting, Link: link, Reason: v1alpha1.ReasonPodPending}
		// A pod nothing will schedule stays Pending with an empty status until join-server gives
		// up; the scheduler's reason is the only thing that says why.
		if reason, message, ok := unschedulable(pod); ok {
			observed.Reason = reason
			observed.Message = fmt.Sprintf("%s: %s", reason, message)
		}
		return observed
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

func unschedulable(pod *corev1.Pod) (string, string, bool) {
	if pod.Status.Phase != corev1.PodPending {
		return "", "", false
	}
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodScheduled && cond.Status == corev1.ConditionFalse && cond.Reason != "" {
			return cond.Reason, cond.Message, true
		}
	}
	return "", "", false
}

func exitMessage(pod *corev1.Pod) string {
	if pod.Status.Phase == corev1.PodSucceeded {
		return "bot process exited"
	}
	return podFailureMessage(pod)
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
