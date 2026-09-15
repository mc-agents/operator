package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	MCPServerPort   = 3000
	DefaultTokenKey = "token"
)

// +kubebuilder:validation:Enum=chat;actionBar;title;dialog;effect;toast
type Feed string

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

type MCPServerAuth struct {
	// A Secret in this namespace that already holds the bearer token. Empty has the operator create
	// <name>-mcp-server-auth once and never rewrite it.
	// +optional
	ExistingSecret string `json:"existingSecret,omitempty"`

	// +optional
	// +kubebuilder:default=token
	SecretKey string `json:"secretKey,omitempty"`
}

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

type MCPServerService struct {
	// +optional
	// +kubebuilder:default=ClusterIP
	// +kubebuilder:validation:Enum=ClusterIP;NodePort;LoadBalancer
	Type corev1.ServiceType `json:"type,omitempty"`

	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

type MCPServerSpec struct {
	// Repository is the full image name. An empty tag is the operator's --mcp-server-tag.
	// +optional
	Image ImageOverride `json:"image,omitempty"`

	// +optional
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`

	// +optional
	Auth MCPServerAuth `json:"auth,omitempty"`

	// +optional
	Bots MCPServerBots `json:"bots,omitempty"`

	// How many bots may be linked at once.
	// +optional
	// +kubebuilder:default=16
	// +kubebuilder:validation:Minimum=1
	MaxBots int32 `json:"maxBots,omitempty"`

	// +optional
	Feeds FeedsSpec `json:"feeds,omitempty"`

	// +optional
	// +kubebuilder:default=INFO
	// +kubebuilder:validation:Enum=TRACE;DEBUG;INFO;WARN;ERROR
	LogLevel string `json:"logLevel,omitempty"`

	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// +optional
	Service MCPServerService `json:"service,omitempty"`

	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`

	// +optional
	PodLabels map[string]string `json:"podLabels,omitempty"`

	// +optional
	PodAnnotations map[string]string `json:"podAnnotations,omitempty"`
}

type SecretKeyRef struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

type MCPServerStatus struct {
	// Where an agent calls, from inside the cluster.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// +optional
	TokenSecretRef *SecretKeyRef `json:"tokenSecretRef,omitempty"`

	// +optional
	Image string `json:"image,omitempty"`

	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

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
