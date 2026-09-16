package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DefaultProfileName is the profile a bot takes when it names none: the namespace's first, then the
// cluster's.
const DefaultProfileName = "default"

// ProfileKind tells a namespace's MinecraftBotProfile from a ClusterMinecraftBotProfile.
//
// +kubebuilder:validation:Enum=MinecraftBotProfile;ClusterMinecraftBotProfile
type ProfileKind string

const (
	ProfileKindNamespaced ProfileKind = "MinecraftBotProfile"
	ProfileKindCluster    ProfileKind = "ClusterMinecraftBotProfile"
)

// ProfileRef names the profile a bot is built from.
type ProfileRef struct {
	// MinecraftBotProfile in the bot's namespace, or ClusterMinecraftBotProfile.
	// +optional
	// +kubebuilder:default=MinecraftBotProfile
	Kind ProfileKind `json:"kind,omitempty"`

	// Name of the profile. One that does not exist makes the bot Failed.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// FabricProfile is what fabric bots take when their own spec leaves it empty.
type FabricProfile struct {
	// Tag of bot-fabric images, before the -mc<version> suffix.
	// +optional
	Tag string `json:"tag,omitempty"`

	// Resources of the bot container.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// What the client draws with.
	// +optional
	Render *RenderSpec `json:"render,omitempty"`

	// Where the client jar and assets come from.
	// +optional
	Assets *AssetCacheSpec `json:"assets,omitempty"`
}

// AzaleaProfile is what azalea bots take when their own spec leaves it empty.
type AzaleaProfile struct {
	// Tag of bot-azalea images, before the -mc<version> suffix.
	// +optional
	Tag string `json:"tag,omitempty"`

	// Resources of the bot container.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

// BotProfileSpec is what a bot takes for every field its own spec leaves empty. Labels and
// annotations merge key by key; every other field is taken whole.
type BotProfileSpec struct {
	// Registry and namespace holding the bot images.
	// +optional
	Registry string `json:"registry,omitempty"`

	// Defaults for fabric bots.
	// +optional
	Fabric FabricProfile `json:"fabric,omitempty"`

	// Defaults for azalea bots.
	// +optional
	Azalea AzaleaProfile `json:"azalea,omitempty"`

	// Goes onto the pod as written.
	// +optional
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`

	// Goes onto the pod as written.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Goes onto the pod as written.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// Goes onto the pod as written.
	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`

	// Labels on the pod, under the bot's own.
	// +optional
	PodLabels map[string]string `json:"podLabels,omitempty"`

	// Annotations on the pod, under the bot's own.
	// +optional
	PodAnnotations map[string]string `json:"podAnnotations,omitempty"`
}

// MinecraftBotProfile holds the defaults the bots of one namespace are built from. The one named
// "default" is taken by every bot in the namespace that names no profile. Changing a profile
// rebuilds the pods of the bots that use it.
//
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

// ClusterMinecraftBotProfile holds the defaults beneath every namespace's. The one named "default"
// is the last layer before the operator's flags.
//
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
