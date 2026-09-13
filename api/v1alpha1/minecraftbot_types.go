package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// One kind of bot exists: a real Minecraft client. There were two, and the machinery for telling
// them apart is kept -- in the catalogue, in the bot protocol and here -- so that a third does not
// have to reinvent it.
//
// +kubebuilder:validation:Enum=fabric
type BotKind string

const (
	BotKindFabric BotKind = "fabric"
)

// +kubebuilder:validation:Enum=Pending;Starting;Running;Failed;Terminating
type BotPhase string

const (
	BotPhasePending     BotPhase = "Pending"
	BotPhaseStarting    BotPhase = "Starting"
	BotPhaseRunning     BotPhase = "Running"
	BotPhaseFailed      BotPhase = "Failed"
	BotPhaseTerminating BotPhase = "Terminating"
)

// +kubebuilder:validation:Enum=Unknown;Waiting;Linked;Lost
type LinkState string

const (
	LinkStateUnknown LinkState = "Unknown"
	LinkStateWaiting LinkState = "Waiting"
	LinkStateLinked  LinkState = "Linked"
	LinkStateLost    LinkState = "Lost"
)

const (
	ConditionReady    = "Ready"
	ConditionDegraded = "Degraded"
)

const DefaultMCPPort = 8765

type MCPServerRef struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Host string `json:"host"`

	// +optional
	// +kubebuilder:default=8765
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port,omitempty"`
}

type ImageOverride struct {
	// +optional
	Repository string `json:"repository,omitempty"`

	// +optional
	Tag string `json:"tag,omitempty"`

	// +optional
	// +kubebuilder:validation:Enum=Always;IfNotPresent;Never
	PullPolicy corev1.PullPolicy `json:"pullPolicy,omitempty"`
}

type RenderSpec struct {
	// +optional
	// +kubebuilder:default=true
	Enabled *bool `json:"enabled,omitempty"`

	// +optional
	// +kubebuilder:default=854
	// +kubebuilder:validation:Minimum=320
	Width int32 `json:"width,omitempty"`

	// +optional
	// +kubebuilder:default=480
	// +kubebuilder:validation:Minimum=240
	Height int32 `json:"height,omitempty"`

	// +optional
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=60
	FrameRateLimit int32 `json:"frameRateLimit,omitempty"`
}

type AssetCacheSpec struct {
	// +optional
	ClaimName string `json:"claimName,omitempty"`

	// +optional
	ReadOnly bool `json:"readOnly,omitempty"`

	// +optional
	Prefilled bool `json:"prefilled,omitempty"`

	// +optional
	FetcherImage string `json:"fetcherImage,omitempty"`

	// +optional
	// +kubebuilder:default="/mc-assets"
	MountPath string `json:"mountPath,omitempty"`

	// +optional
	SizeLimit *resource.Quantity `json:"sizeLimit,omitempty"`

	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="self.kind == 'fabric' || !has(self.render)",message="render is fabric-only"
// +kubebuilder:validation:XValidation:rule="self.kind == 'fabric' || !has(self.assets)",message="assets is fabric-only"
type MinecraftBotSpec struct {
	// +kubebuilder:default=fabric
	// +optional
	Kind BotKind `json:"kind,omitempty"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^[0-9]+\.[0-9]+(\.[0-9]+)?$`
	MinecraftVersion string `json:"minecraftVersion"`

	// +kubebuilder:validation:Required
	Server MCPServerRef `json:"server"`

	// +optional
	// +kubebuilder:validation:MaxLength=16
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9_]{1,16}$`
	BotName string `json:"botName,omitempty"`

	// +optional
	Render *RenderSpec `json:"render,omitempty"`

	// +optional
	Assets *AssetCacheSpec `json:"assets,omitempty"`

	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// +optional
	Image ImageOverride `json:"image,omitempty"`

	// +optional
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`

	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`

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

	// +optional
	// +kubebuilder:validation:Minimum=0
	TerminationGracePeriodSeconds *int64 `json:"terminationGracePeriodSeconds,omitempty"`
}

type MinecraftBotStatus struct {
	// +optional
	Phase BotPhase `json:"phase,omitempty"`

	// +optional
	Link LinkState `json:"link,omitempty"`

	// +optional
	PodName string `json:"podName,omitempty"`

	// +optional
	Image string `json:"image,omitempty"`

	// +optional
	LastError string `json:"lastError,omitempty"`

	// +optional
	LastSpawnTime *metav1.Time `json:"lastSpawnTime,omitempty"`

	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=mcbot,categories=mc-agents
// +kubebuilder:printcolumn:name="Kind",type=string,JSONPath=`.spec.kind`
// +kubebuilder:printcolumn:name="MC",type=string,JSONPath=`.spec.minecraftVersion`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Link",type=string,JSONPath=`.status.link`
// +kubebuilder:printcolumn:name="Pod",type=string,JSONPath=`.status.podName`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:printcolumn:name="Error",type=string,JSONPath=`.status.lastError`,priority=1
// +kubebuilder:printcolumn:name="Image",type=string,JSONPath=`.status.image`,priority=1
type MinecraftBot struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MinecraftBotSpec   `json:"spec,omitempty"`
	Status MinecraftBotStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type MinecraftBotList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MinecraftBot `json:"items"`
}
