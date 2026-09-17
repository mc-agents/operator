package bot_test

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/mc-agents/operator/api/v1alpha1"
	botctl "github.com/mc-agents/operator/internal/controller/bot"
)

func TestObserve(t *testing.T) {
	cases := []struct {
		name     string
		pod      *corev1.Pod
		phase    v1alpha1.BotPhase
		link     v1alpha1.LinkState
		reason   string
		contains string
	}{
		{
			name:   "no pod yet",
			pod:    nil,
			phase:  v1alpha1.BotPhasePending,
			link:   v1alpha1.LinkStateUnknown,
			reason: v1alpha1.ReasonPodPending,
		},
		{
			name: "a missing image is reported instead of looking like a slow start",
			pod: pending(corev1.ContainerStatus{
				Name: "bot",
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
					Reason:  "ImagePullBackOff",
					Message: `Back-off pulling image "junhyung.cloud/mc-agents/bot-fabric:0.2.0-mc26.1.2"`,
				}},
			}),
			phase:    v1alpha1.BotPhaseFailed,
			link:     v1alpha1.LinkStateUnknown,
			reason:   "ImagePullBackOff",
			contains: "ImagePullBackOff",
		},
		{
			name: "an init container that cannot pull blocks the bot too",
			pod: func() *corev1.Pod {
				p := pending()
				p.Status.InitContainerStatuses = []corev1.ContainerStatus{{
					Name:  "fetch-assets",
					State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ErrImagePull", Message: "no such image"}},
				}}
				return p
			}(),
			phase:    v1alpha1.BotPhaseFailed,
			link:     v1alpha1.LinkStateUnknown,
			reason:   "ErrImagePull",
			contains: "ErrImagePull",
		},
		{
			name: "a pod nothing will schedule says so while it stays Starting",
			pod: func() *corev1.Pod {
				p := pending()
				p.Status.Conditions = []corev1.PodCondition{{
					Type:    corev1.PodScheduled,
					Status:  corev1.ConditionFalse,
					Reason:  corev1.PodReasonUnschedulable,
					Message: "0/1 nodes are available: 1 Insufficient memory.",
				}}
				return p
			}(),
			phase:    v1alpha1.BotPhaseStarting,
			link:     v1alpha1.LinkStateUnknown,
			reason:   corev1.PodReasonUnschedulable,
			contains: "Unschedulable: 0/1 nodes are available: 1 Insufficient memory.",
		},
		{
			name:   "running but not ready means the link is not up yet",
			pod:    running(false, 0),
			phase:  v1alpha1.BotPhaseRunning,
			link:   v1alpha1.LinkStateWaiting,
			reason: v1alpha1.ReasonWaitingForLink,
		},
		{
			name:   "ready means the bot said hello to the MCP server",
			pod:    running(true, 0),
			phase:  v1alpha1.BotPhaseRunning,
			link:   v1alpha1.LinkStateLinked,
			reason: v1alpha1.ReasonLinked,
		},
		{
			name:   "a restarted container that is not ready again is a lost link",
			pod:    running(false, 3),
			phase:  v1alpha1.BotPhaseRunning,
			link:   v1alpha1.LinkStateLost,
			reason: v1alpha1.ReasonWaitingForLink,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := botctl.Observe(tc.pod)
			if got.Phase != tc.phase {
				t.Errorf("phase is %q, want %q", got.Phase, tc.phase)
			}
			if got.Link != tc.link {
				t.Errorf("link is %q, want %q", got.Link, tc.link)
			}
			if got.Reason != tc.reason {
				t.Errorf("reason is %q, want %q", got.Reason, tc.reason)
			}
			if tc.contains != "" && !strings.Contains(got.Message, tc.contains) {
				t.Errorf("message %q does not mention %q", got.Message, tc.contains)
			}
		})
	}
}

func TestObserveCrashLoopBeatsRunning(t *testing.T) {
	pod := running(false, 5)
	pod.Status.ContainerStatuses[0].State = corev1.ContainerState{
		Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff", Message: "back-off 5m0s"},
	}
	pod.Status.ContainerStatuses[0].LastTerminationState = corev1.ContainerState{
		Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Reason: "Error", Message: "fetch failed: connection reset"},
	}
	got := botctl.Observe(pod)
	if got.Phase != v1alpha1.BotPhaseFailed {
		t.Fatalf("phase is %q, want Failed", got.Phase)
	}
	if !strings.Contains(got.Message, "CrashLoopBackOff") {
		t.Fatalf("message %q does not name the reason", got.Message)
	}
	if !strings.Contains(got.Message, "exited with code 1 (Error): fetch failed: connection reset") {
		t.Fatalf("message %q does not say what the container said as it exited", got.Message)
	}
}

func TestObserveAFirstCrashIsStillStarting(t *testing.T) {
	pod := pending()
	pod.Status.InitContainerStatuses = []corev1.ContainerStatus{{
		Name:         "fetch-assets",
		RestartCount: 1,
		State: corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff", Message: "back-off 10s"},
		},
	}}
	got := botctl.Observe(pod)
	if got.Phase == v1alpha1.BotPhaseFailed {
		t.Fatalf("one crash is a restart the kubelet is already making, not a failure: %q", got.Message)
	}
}

func pending(statuses ...corev1.ContainerStatus) *corev1.Pod {
	return &corev1.Pod{Status: corev1.PodStatus{
		Phase:             corev1.PodPending,
		ContainerStatuses: statuses,
	}}
}

func running(ready bool, restarts int32) *corev1.Pod {
	condition := corev1.ConditionFalse
	if ready {
		condition = corev1.ConditionTrue
	}
	return &corev1.Pod{Status: corev1.PodStatus{
		Phase:      corev1.PodRunning,
		Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: condition}},
		ContainerStatuses: []corev1.ContainerStatus{{
			Name:         "bot",
			Ready:        ready,
			RestartCount: restarts,
			State:        corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		}},
	}}
}
