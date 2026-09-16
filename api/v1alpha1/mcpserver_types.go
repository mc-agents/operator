package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	MCPServerPort   = 3000
	DefaultTokenKey = "token"
)

// Feed is one of the streams a bot relays from the game.
//
// +kubebuilder:validation:Enum=chat;actionBar;title;dialog;effect;toast
type Feed string

// FeedsSpec tunes what the bots relay from the game.
type FeedsSpec struct {
	// How often a repeated action bar, title or dialog line is resent. It is closed after three
	// intervals without a repeat.
	// +optional
	// +kubebuilder:default=1000
	// +kubebuilder:validation:Minimum=50
	RepeatFlushMs int32 `json:"repeatFlushMs,omitempty"`

	// Feeds the bots never send.
	// +optional
	// +listType=set
	Muted []Feed `json:"muted,omitempty"`
}

// MCPServerAuth is where the bearer token agents present on the MCP port comes from.
type MCPServerAuth struct {
	// A Secret in this namespace that already holds the bearer token. Empty has the operator create
	// <name>-mcp-server-auth once and never rewrite it.
	// +optional
	ExistingSecret string `json:"existingSecret,omitempty"`

	// The key in that Secret holding the token.
	// +optional
	// +kubebuilder:default=token
	SecretKey string `json:"secretKey,omitempty"`
}

// MCPServerBots is how the server gets the bots it links.
type MCPServerBots struct {
	// Whether join-server may create a MinecraftBot in this namespace when none is running under the
	// name it was given. Off, it uses bots that dial in and nothing else, and the Role goes with it.
	// +optional
	// +kubebuilder:default=true
	Provision *bool `json:"provision,omitempty"`

	// Written into every MinecraftBot this server creates.
	// +optional
	ProfileRef *ProfileRef `json:"profileRef,omitempty"`
}

// MCPServerService shapes the Service carrying the MCP port. The bot-link port is on a separate
// ClusterIP Service whatever is set here, so it never leaves the cluster.
type MCPServerService struct {
	// Type of the Service carrying the MCP port; the bearer token is that port's gate.
	// +optional
	// +kubebuilder:default=ClusterIP
	// +kubebuilder:validation:Enum=ClusterIP;NodePort;LoadBalancer
	Type corev1.ServiceType `json:"type,omitempty"`

	// Goes onto the Service as written, for a load balancer or DNS controller.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// MCPServerSpec describes one MCP server, run as a single-replica Deployment in this namespace.
type MCPServerSpec struct {
	// Repository is the full image name. An empty tag is the operator's --mcp-server-tag.
	// +optional
	Image ImageOverride `json:"image,omitempty"`

	// Goes onto the pod as written.
	// +optional
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`

	// The bearer token agents present.
	// +optional
	Auth MCPServerAuth `json:"auth,omitempty"`

	// How the server gets its bots.
	// +optional
	Bots MCPServerBots `json:"bots,omitempty"`

	// How many bots may be linked at once.
	// +optional
	// +kubebuilder:default=16
	// +kubebuilder:validation:Minimum=1
	MaxBots int32 `json:"maxBots,omitempty"`

	// What the bots relay from the game.
	// +optional
	Feeds FeedsSpec `json:"feeds,omitempty"`

	// Log level of the server process.
	// +optional
	// +kubebuilder:default=INFO
	// +kubebuilder:validation:Enum=TRACE;DEBUG;INFO;WARN;ERROR
	LogLevel string `json:"logLevel,omitempty"`

	// Structured log format of the server process, for a log pipeline that indexes fields; empty
	// keeps the plain console lines.
	// +optional
	// +kubebuilder:validation:Enum=ecs;logstash;gelf
	LogFormat string `json:"logFormat,omitempty"`

	// Resources of the server container; empty is 100m/512Mi requested with a 1Gi limit.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// The Service carrying the MCP port.
	// +optional
	Service MCPServerService `json:"service,omitempty"`

	// Goes onto the pod as written.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Goes onto the pod as written.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// Goes onto the pod as written.
	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`

	// Extra labels on the pod; the operator's own labels win.
	// +optional
	PodLabels map[string]string `json:"podLabels,omitempty"`

	// Extra annotations on the pod.
	// +optional
	PodAnnotations map[string]string `json:"podAnnotations,omitempty"`
}

// SecretKeyRef names one key of one Secret in the MCPServer's namespace.
type SecretKeyRef struct {
	// Name of the Secret.
	Name string `json:"name"`
	// Key inside it.
	Key string `json:"key"`
}

// MCPServerStatus is where the server is and whether it is accepting bots.
type MCPServerStatus struct {
	// Where an agent calls, from inside the cluster.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// The Secret and key holding the bearer token, generated or not.
	// +optional
	TokenSecretRef *SecretKeyRef `json:"tokenSecretRef,omitempty"`

	// The image the server runs.
	// +optional
	Image string `json:"image,omitempty"`

	// The generation of the spec this status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Ready is True once the server pod is available; False names what the kubelet or the
	// Deployment is stuck on.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// MCPServer is one MCP server with its own token, run in the namespace that asks for it. Everything
// it makes is named <name>-mcp-server and owned by it.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=mcps,categories=mc-agents
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=`.status.endpoint`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:printcolumn:name="Image",type=string,JSONPath=`.status.image`,priority=1
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].message`,priority=1
type MCPServer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MCPServerSpec   `json:"spec,omitempty"`
	Status MCPServerStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type MCPServerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MCPServer `json:"items"`
}
