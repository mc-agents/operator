package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DefaultProfileName is the profile a bot takes when it names none: the namespace's first, then the
// cluster's.
const DefaultProfileName = "default"

// +kubebuilder:validation:Enum=MinecraftBotProfile;ClusterMinecraftBotProfile
type ProfileKind string

const (
	ProfileKindNamespaced ProfileKind = "MinecraftBotProfile"
	ProfileKindCluster    ProfileKind = "ClusterMinecraftBotProfile"
)

type ProfileRef struct {
	// +optional
	// +kubebuilder:default=MinecraftBotProfile
	Kind ProfileKind `json:"kind,omitempty"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

type FabricProfile struct {
	// Tag of bot-fabric images, before the -mc<version> suffix.
	// +optional
	Tag string `json:"tag,omitempty"`

	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// +optional
	Render *RenderSpec `json:"render,omitempty"`

	// +optional
	Assets *AssetCacheSpec `json:"assets,omitempty"`
}

type AzaleaProfile struct {
	// Tag of bot-azalea images, before the -mc<version> suffix.
	// +optional
	Tag string `json:"tag,omitempty"`

	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

// BotProfileSpec is what a bot takes for every field its own spec leaves empty.
type BotProfileSpec struct {
	// Registry and namespace holding the bot images.
	// +optional
	Registry string `json:"registry,omitempty"`

	// +optional
	Fabric FabricProfile `json:"fabric,omitempty"`

	// +optional
	Azalea AzaleaProfile `json:"azalea,omitempty"`

	// +optional
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`

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

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=mcbotprofile,categories=mc-agents
// +kubebuilder:printcolumn:name="Fabric",type=string,JSONPath=`.spec.fabric.tag`
// +kubebuilder:printcolumn:name="Azalea",type=string,JSONPath=`.spec.azalea.tag`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type MinecraftBotProfile struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec BotProfileSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true
type MinecraftBotProfileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MinecraftBotProfile `json:"items"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=cmcbotprofile,categories=mc-agents
// +kubebuilder:printcolumn:name="Fabric",type=string,JSONPath=`.spec.fabric.tag`
// +kubebuilder:printcolumn:name="Azalea",type=string,JSONPath=`.spec.azalea.tag`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type ClusterMinecraftBotProfile struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec BotProfileSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true
type ClusterMinecraftBotProfileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterMinecraftBotProfile `json:"items"`
}
