package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type MinecraftBotTemplate struct {
	// +optional
	Metadata TemplateMetadata `json:"metadata,omitempty"`

	// +kubebuilder:validation:Required
	Spec MinecraftBotSpec `json:"spec"`
}

type TemplateMetadata struct {
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

type MinecraftBotPoolSpec struct {
	// +optional
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=0
	Replicas *int32 `json:"replicas,omitempty"`

	// +kubebuilder:validation:Required
	Template MinecraftBotTemplate `json:"template"`
}

type MinecraftBotPoolStatus struct {
	// +optional
	Replicas int32 `json:"replicas"`

	// +optional
	ReadyReplicas int32 `json:"readyReplicas"`

	// +optional
	LinkedReplicas int32 `json:"linkedReplicas"`

	// +optional
	Selector string `json:"selector,omitempty"`

	// +optional
	LastError string `json:"lastError,omitempty"`

	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

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
