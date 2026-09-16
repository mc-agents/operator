// Package podstatus reads what the kubelet says about a pod that is not coming up, for the bot and
// MCPServer reconcilers to lift into their own status rather than leaving it in the pod.
package podstatus

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
)

var terminalWaitReasons = map[string]bool{
	"ImagePullBackOff":           true,
	"ErrImagePull":               true,
	"InvalidImageName":           true,
	"CreateContainerConfigError": true,
	"CreateContainerError":       true,
	"CrashLoopBackOff":           true,
}

// A crash loop is only a failure once it has repeated. The kubelet reports the back-off after the
// first exit, and calling that Failed made the MCP server give the bot back: the pod went, a
// transient first run of the asset fetcher took its only log with it, and the same bot asked for
// again filled its cache and linked.
const crashLoopRestarts = 3

// Blocked reports the first container the kubelet cannot bring up, as its waiting reason and a
// message that carries what the container said as it last exited.
func Blocked(pod *corev1.Pod) (reason, message string, ok bool) {
	all := make([]corev1.ContainerStatus, 0, len(pod.Status.InitContainerStatuses)+len(pod.Status.ContainerStatuses))
	all = append(all, pod.Status.InitContainerStatuses...)
	all = append(all, pod.Status.ContainerStatuses...)
	for _, status := range all {
		waiting := status.State.Waiting
		if waiting == nil || !terminalWaitReasons[waiting.Reason] {
			continue
		}
		if waiting.Reason == "CrashLoopBackOff" && status.RestartCount < crashLoopRestarts {
			continue
		}
		message := waiting.Message
		if message == "" {
			message = status.Name
		}
		if exited := lastExit(status); exited != "" {
			message += "; " + exited
		}
		return waiting.Reason, message, true
	}
	return "", "", false
}

// What the container said as it last exited. With FallbackToLogsOnError on the container, the
// kubelet puts the tail of its log in the message, which outlives the pod.
func lastExit(status corev1.ContainerStatus) string {
	t := status.LastTerminationState.Terminated
	if t == nil {
		return ""
	}
	said := fmt.Sprintf("%s last exited with code %d (%s)", status.Name, t.ExitCode, t.Reason)
	if t.Message != "" {
		said += ": " + t.Message
	}
	return said
}
