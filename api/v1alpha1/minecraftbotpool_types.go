package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// MinecraftBotTemplate is the MinecraftBot every replica is made from.
type MinecraftBotTemplate struct {
	// Labels and annotations put on every bot of the pool.
	// +optional
	Metadata TemplateMetadata `json:"metadata,omitempty"`

	// Spec of every bot of the pool. Editing it rewrites the bots in place, and each bot then
	// replaces its own pod.
	// +kubebuilder:validation:Required
	Spec MinecraftBotSpec `json:"spec"`
}

// TemplateMetadata is the part of a bot's metadata a pool template may set.
type TemplateMetadata struct {
	// Labels on every bot; the operator's pool and managed-by labels win.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// Annotations on every bot.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// MinecraftBotPoolSpec is a replica count and the bot it is made of.
//
// +kubebuilder:validation:XValidation:rule="!has(self.template.spec.botName)",message="botName is per bot; pool bots dial in as <pool>-<ordinal>"
type MinecraftBotPoolSpec struct {
	// How many bots the pool keeps, named <pool>-0 upwards. Scaling down removes the highest
	// ordinals, so a name an agent holds survives as long as its ordinal does.
	// +optional
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=0
	Replicas *int32 `json:"replicas,omitempty"`

	// The bot every replica is made from. botName must be empty: three replicas dialling in under
	// one name would leave two of them Waiting forever.
	// +kubebuilder:validation:Required
	Template MinecraftBotTemplate `json:"template"`
}

// MinecraftBotPoolStatus rolls the bots' status up into the counters behind the scale subresource.
type MinecraftBotPoolStatus struct {
	// Bots the pool currently owns.
	// +optional
	Replicas int32 `json:"replicas"`

	// Bots whose pod is Running.
	// +optional
	ReadyReplicas int32 `json:"readyReplicas"`

	// Bots the MCP server has accepted.
	// +optional
	LinkedReplicas int32 `json:"linkedReplicas"`

	// Label selector matching the pool's bots, for kubectl scale and HPA.
	// +optional
	Selector string `json:"selector,omitempty"`

	// The first bot's lastError, prefixed with its name, or a bot of a pool name that is not the
	// pool's to take.
	// +optional
	LastError string `json:"lastError,omitempty"`

	// The generation of the spec this status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Ready is True once every replica is Linked.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// MinecraftBotPool keeps a replica count of ordinal-named MinecraftBots, so scaling the bots is
// one field rather than one CR per bot.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:subresource:scale:specpath=.spec.replicas,statuspath=.status.replicas,selectorpath=.status.selector
// +kubebuilder:resource:scope=Namespaced,shortName=mcbotpool,categories=mc-agents
// +kubebuilder:printcolumn:name="Desired",type=integer,JSONPath=`.spec.replicas`
// +kubebuilder:printcolumn:name="Current",type=integer,JSONPath=`.status.replicas`
// +kubebuilder:printcolumn:name="Linked",type=integer,JSONPath=`.status.linkedReplicas`
// +kubebuilder:printcolumn:name="Kind",type=string,JSONPath=`.spec.template.spec.kind`
// +kubebuilder:printcolumn:name="MC",type=string,JSONPath=`.spec.template.spec.minecraftVersion`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:printcolumn:name="Error",type=string,JSONPath=`.status.lastError`,priority=1
type MinecraftBotPool struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MinecraftBotPoolSpec   `json:"spec,omitempty"`
	Status MinecraftBotPoolStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type MinecraftBotPoolList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MinecraftBotPool `json:"items"`
}
