package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// fabric is a real Minecraft client: it renders, takes screenshots and draws a resource pack, and
// costs about 2GiB. azalea speaks the protocol without a client, at a few MiB, for the many bots a
// scenario can need at once and for the tools that do not need to see anything.
//
// +kubebuilder:validation:Enum=fabric;azalea
type BotKind string

const (
	BotKindFabric BotKind = "fabric"
	BotKindAzalea BotKind = "azalea"
)

// BotPhase is about the pod: Pending before it is created, Starting until its containers run,
// Running once they do, Failed when the kubelet cannot bring it up or the process has exited, and
// Terminating while the MinecraftBot is being deleted.
//
// +kubebuilder:validation:Enum=Pending;Starting;Running;Failed;Terminating
type BotPhase string

const (
	BotPhasePending     BotPhase = "Pending"
	BotPhaseStarting    BotPhase = "Starting"
	BotPhaseRunning     BotPhase = "Running"
	BotPhaseFailed      BotPhase = "Failed"
	BotPhaseTerminating BotPhase = "Terminating"
)

// LinkState is about the bot, inferred from pod readiness: the readiness probe goes green only once
// the MCP server has accepted the bot's hello. Waiting is a running process not yet accepted, Lost
// a container that restarted and has not linked again, Unknown a pod that is not running.
//
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

// Reasons the MinecraftBot conditions carry. Ready is True only under ReasonLinked; Degraded is
// True under whichever of the rest names the cause, and the kubelet's own waiting reason
// (ImagePullBackOff, CrashLoopBackOff, ...) is used verbatim when a container is blocked.
const (
	ReasonLinked         = "Linked"
	ReasonWaitingForLink = "WaitingForLink"
	ReasonPodPending     = "PodPending"
	ReasonPodFailed      = "PodFailed"
	ReasonPodExited      = "PodExited"
	ReasonInvalidSpec    = "InvalidSpec"
	ReasonPodNotOwned    = "PodNotOwned"
	ReasonTerminating    = "Terminating"
)

const DefaultMCPPort = 8765

// MCPServerRef is where the bot dials in. For an operator-managed MCPServer it is the
// <name>-mcp-server-bots Service in the server's namespace.
type MCPServerRef struct {
	// Host the bot dials, handed to the pod as MCP_SERVER_HOST.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Host string `json:"host"`

	// Bot-link port of the MCP server, handed to the pod as MCP_SERVER_PORT.
	// +optional
	// +kubebuilder:default=8765
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port,omitempty"`
}

// LinkTokenSecretRef names the Secret a bot presents in its hello, so that a pod which gets past
// the NetworkPolicy still cannot pass itself off as a bot. For an operator-managed MCPServer it is
// <name>-mcp-server-link, which join-server writes into every bot it creates.
type LinkTokenSecretRef struct {
	// Name of the Secret in the bot's namespace.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// The key in that Secret holding the token.
	// +optional
	// +kubebuilder:default=token
	Key string `json:"key,omitempty"`
}

// ImageOverride replaces the image the profiles and flags would build. A repository that already
// carries a tag or a digest is used verbatim.
type ImageOverride struct {
	// Full image name without a tag; empty builds <registry>/bot-<kind> from the nearest profile.
	// +optional
	Repository string `json:"repository,omitempty"`

	// Used as written: unlike a profile's tag, no -mc<minecraftVersion> is appended.
	// +optional
	Tag string `json:"tag,omitempty"`

	// Goes onto the pod as written.
	// +optional
	// +kubebuilder:validation:Enum=Always;IfNotPresent;Never
	PullPolicy corev1.PullPolicy `json:"pullPolicy,omitempty"`
}

// RenderSpec is what a fabric bot draws with. It is handed to the pod as BOT_RENDER,
// BOT_RENDER_WIDTH, BOT_RENDER_HEIGHT and BOT_FRAME_RATE_LIMIT.
type RenderSpec struct {
	// Whether the client renders at all. Off, it cannot take a screenshot.
	// +optional
	// +kubebuilder:default=true
	Enabled *bool `json:"enabled,omitempty"`

	// Width of the framebuffer in pixels.
	// +optional
	// +kubebuilder:default=854
	// +kubebuilder:validation:Minimum=320
	Width int32 `json:"width,omitempty"`

	// Height of the framebuffer in pixels.
	// +optional
	// +kubebuilder:default=480
	// +kubebuilder:validation:Minimum=240
	Height int32 `json:"height,omitempty"`

	// Frames per second the client renders. One is enough for screenshots and keeps the CPU down.
	// +optional
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=60
	FrameRateLimit int32 `json:"frameRateLimit,omitempty"`
}

// AssetCacheSpec is where a fabric bot's client jar and assets live. They are not ours to
// redistribute in an image, so an initContainer pulls them from the Mojang version manifest.
type AssetCacheSpec struct {
	// A PersistentVolumeClaim holding the assets; empty uses an emptyDir and every pod re-downloads
	// 200-600MB. Fill one PVC per Minecraft version with a Job and share it.
	// +optional
	ClaimName string `json:"claimName,omitempty"`

	// The bot container mounts the assets read-only. The claim itself is mounted read-only only
	// together with prefilled, since the fetcher has to write into it otherwise.
	// +optional
	ReadOnly bool `json:"readOnly,omitempty"`

	// The claim already holds the assets: skip the initContainer and pod start is a mount.
	// +optional
	Prefilled bool `json:"prefilled,omitempty"`

	// Image of the initContainer that fetches the assets; empty uses the operator's
	// --asset-fetcher-image with -mc<minecraftVersion> appended.
	// +optional
	FetcherImage string `json:"fetcherImage,omitempty"`

	// Where the volume is mounted in the pod; the assets for a version are under
	// <mountPath>/<minecraftVersion>, which the bot is handed as MC_ASSETS_DIR.
	// +optional
	// +kubebuilder:default="/mc-assets"
	MountPath string `json:"mountPath,omitempty"`

	// Size limit of the emptyDir used when no claim is named.
	// +optional
	SizeLimit *resource.Quantity `json:"sizeLimit,omitempty"`

	// Resources of the fetcher initContainer; goes onto the pod as written.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

// MinecraftBotSpec describes one bot process. Every field left empty is taken from the nearest
// profile, then from the operator's flags.
//
// +kubebuilder:validation:XValidation:rule="self.kind == 'fabric' || !has(self.render)",message="render is fabric-only"
// +kubebuilder:validation:XValidation:rule="self.kind == 'fabric' || !has(self.assets)",message="assets is fabric-only"
type MinecraftBotSpec struct {
	// Which bot runs: fabric is a real client that renders, azalea speaks the protocol without one.
	// +kubebuilder:default=fabric
	// +optional
	Kind BotKind `json:"kind,omitempty"`

	// Minecraft version the bot claims and the image is built for, appended to the image tag as
	// -mc<minecraftVersion>.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^[0-9]+\.[0-9]+(\.[0-9]+)?$`
	MinecraftVersion string `json:"minecraftVersion"`

	// The MCP server the bot dials. There is no game server address: the bot learns that from the
	// MCP server after the handshake.
	// +kubebuilder:validation:Required
	Server MCPServerRef `json:"server"`

	// The Secret holding the token the bot presents in hello, handed to the pod as BOT_LINK_TOKEN.
	// Empty sends none, which an MCP server with a token configured refuses.
	// +optional
	LinkTokenSecretRef *LinkTokenSecretRef `json:"linkTokenSecretRef,omitempty"`

	// The name the bot reports in hello and agents address it by; empty uses the CR name
	// truncated to 16 characters. Pool bots must leave it empty and are named <pool>-<ordinal>.
	// +optional
	// +kubebuilder:validation:MaxLength=16
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9_]{1,16}$`
	BotName string `json:"botName,omitempty"`

	// Fills whatever this spec leaves empty. Without one the bot takes the namespace's
	// MinecraftBotProfile "default", then the ClusterMinecraftBotProfile "default". A profile
	// named here that does not exist makes the bot Failed rather than built from the defaults.
	// +optional
	ProfileRef *ProfileRef `json:"profileRef,omitempty"`

	// What a fabric bot draws with; rejected on any other kind.
	// +optional
	Render *RenderSpec `json:"render,omitempty"`

	// Where a fabric bot's client jar and assets come from; rejected on any other kind.
	// +optional
	Assets *AssetCacheSpec `json:"assets,omitempty"`

	// Resources of the bot container; empty takes the profile's for this kind, then a measured
	// default (about 2Gi for fabric, 64Mi for azalea).
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// Overrides the image the profiles and flags would build.
	// +optional
	Image ImageOverride `json:"image,omitempty"`

	// Goes onto the pod as written; empty takes the profile's.
	// +optional
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`

	// ServiceAccount of the pod; empty runs with none and no token mounted.
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	// Appended to the bot container's environment. The names the operator sets itself are
	// reserved, since a later entry would silently win.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:XValidation:rule="!self.exists(e, e.name in ['MCP_SERVER_HOST','MCP_SERVER_PORT','BOT_LINK_TOKEN','BOT_NAME','BOT_KIND','MC_VERSION','HEALTH_PORT','BOT_WORK_DIR','MC_ASSETS_DIR','BOT_RENDER','BOT_RENDER_WIDTH','BOT_RENDER_HEIGHT','BOT_FRAME_RATE_LIMIT','POD_NAME','POD_NAMESPACE','NODE_NAME'])",message="env names the operator sets are reserved"
	Env []corev1.EnvVar `json:"env,omitempty"`

	// Goes onto the pod as written; empty takes the profile's.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Goes onto the pod as written; empty takes the profile's.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// Goes onto the pod as written; empty takes the profile's.
	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`

	// Extra labels on the pod, merged key by key over the profile's; the operator's own labels win.
	// +optional
	PodLabels map[string]string `json:"podLabels,omitempty"`

	// Extra annotations on the pod, merged key by key over the profile's.
	// +optional
	PodAnnotations map[string]string `json:"podAnnotations,omitempty"`

	// Goes onto the pod as written; empty is 30 seconds.
	// +optional
	// +kubebuilder:validation:Minimum=0
	TerminationGracePeriodSeconds *int64 `json:"terminationGracePeriodSeconds,omitempty"`
}

// MinecraftBotStatus mirrors what the pod is doing. Phase is about the pod, Link about the bot.
type MinecraftBotStatus struct {
	// What the pod is doing: Pending, Starting, Running, Failed or Terminating.
	// +optional
	Phase BotPhase `json:"phase,omitempty"`

	// Whether the MCP server has the bot in its registry, inferred from pod readiness.
	// +optional
	Link LinkState `json:"link,omitempty"`

	// The pod the bot runs in; the same name as the MinecraftBot.
	// +optional
	PodName string `json:"podName,omitempty"`

	// The image the pod runs, after profiles and overrides were applied.
	// +optional
	Image string `json:"image,omitempty"`

	// The profiles the pod was built from, nearest first.
	// +optional
	Profiles []string `json:"profiles,omitempty"`

	// Why the bot is not linked, when the operator knows: a kubelet reason like ImagePullBackOff,
	// what the container said as it exited, a profile that does not exist, or a pod of this name
	// that is not the bot's. Empty while the bot waits its turn under --spawn-interval.
	// +optional
	LastError string `json:"lastError,omitempty"`

	// When the operator last created a pod for this bot.
	// +optional
	LastSpawnTime *metav1.Time `json:"lastSpawnTime,omitempty"`

	// The generation of the spec this status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Ready is True only while the bot is Linked. Degraded is True while the phase is Failed, with
	// the reason naming the cause.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// MinecraftBot is one bot process in one pod, which the operator recreates when it dies.
//
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
