package podspec

import (
	"fmt"
	"maps"
	"path"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/mc-agents/operator/api/v1alpha1"
	"github.com/mc-agents/operator/internal/botimage"
)

const (
	ContainerName       = "bot"
	InitContainerName   = "fetch-assets"
	HealthPortName      = "health"
	HealthPort          = 8080
	WorkDir             = "/work"
	DefaultAssetsMount  = "/mc-assets"
	volumeWork          = "work"
	volumeTmp           = "tmp"
	volumeAssets        = "assets"
	defaultGracePeriod  = 30
	defaultFetcherImage = botimage.DefaultRegistry + "/mc-assets:latest"
)

type Builder interface {
	Build(bot *v1alpha1.MinecraftBot) (*corev1.Pod, error)
}

type Defaults struct {
	AssetFetcherImage string
	MineflayerImage   string
	FabricImage       string
}

type DefaultBuilder struct {
	images   botimage.Resolver
	defaults Defaults
}

func NewBuilder(images botimage.Resolver, defaults Defaults) *DefaultBuilder {
	if defaults.AssetFetcherImage == "" {
		defaults.AssetFetcherImage = defaultFetcherImage
	}
	return &DefaultBuilder{images: images, defaults: defaults}
}

func (b *DefaultBuilder) Build(bot *v1alpha1.MinecraftBot) (*corev1.Pod, error) {
	image, err := b.images.Resolve(&bot.Spec)
	if err != nil {
		return nil, fmt.Errorf("resolve image: %w", err)
	}

	spec := corev1.PodSpec{
		RestartPolicy:                 corev1.RestartPolicyAlways,
		EnableServiceLinks:            ptr(false),
		AutomountServiceAccountToken:  ptr(bot.Spec.ServiceAccountName != ""),
		ServiceAccountName:            bot.Spec.ServiceAccountName,
		TerminationGracePeriodSeconds: gracePeriod(bot),
		ImagePullSecrets:              bot.Spec.ImagePullSecrets,
		NodeSelector:                  bot.Spec.NodeSelector,
		Tolerations:                   bot.Spec.Tolerations,
		Affinity:                      bot.Spec.Affinity,
		SecurityContext: &corev1.PodSecurityContext{
			RunAsNonRoot:   ptr(true),
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
		Volumes: []corev1.Volume{
			emptyDir(volumeWork, nil),
			emptyDir(volumeTmp, nil),
		},
		Containers: []corev1.Container{b.container(bot, image)},
	}

	if bot.Spec.Kind == v1alpha1.BotKindFabric {
		assets := assetSpec(bot)
		spec.Volumes = append(spec.Volumes, assetVolume(assets))
		if !assets.Prefilled {
			spec.InitContainers = append(spec.InitContainers, b.assetFetcher(bot, assets))
		}
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        bot.Name,
			Namespace:   bot.Namespace,
			Labels:      podLabels(bot),
			Annotations: podAnnotations(bot),
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(bot, v1alpha1.SchemeGroupVersion.WithKind("MinecraftBot")),
			},
		},
		Spec: spec,
	}
	pod.Annotations[v1alpha1.AnnotationSpecHash] = Hash(&pod.Spec)
	return pod, nil
}

func (b *DefaultBuilder) container(bot *v1alpha1.MinecraftBot, image string) corev1.Container {
	c := corev1.Container{
		Name:            ContainerName,
		Image:           image,
		ImagePullPolicy: bot.Spec.Image.PullPolicy,
		Env:             containerEnv(bot),
		Ports: []corev1.ContainerPort{
			{Name: HealthPortName, ContainerPort: HealthPort, Protocol: corev1.ProtocolTCP},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: volumeWork, MountPath: WorkDir},
			{Name: volumeTmp, MountPath: "/tmp"},
		},
		Resources:      resources(bot),
		StartupProbe:   startupProbe(bot.Spec.Kind),
		ReadinessProbe: readinessProbe(),
		LivenessProbe:  livenessProbe(),
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptr(false),
			ReadOnlyRootFilesystem:   ptr(true),
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		},
	}
	if bot.Spec.Kind == v1alpha1.BotKindFabric {
		assets := assetSpec(bot)
		c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{
			Name:      volumeAssets,
			MountPath: assets.MountPath,
			ReadOnly:  assets.ReadOnly,
		})
	}
	return c
}

func (b *DefaultBuilder) assetFetcher(bot *v1alpha1.MinecraftBot, assets *v1alpha1.AssetCacheSpec) corev1.Container {
	image := assets.FetcherImage
	if image == "" {
		image = b.defaults.AssetFetcherImage
	}
	return corev1.Container{
		Name:            InitContainerName,
		Image:           image,
		ImagePullPolicy: bot.Spec.Image.PullPolicy,
		Args: []string{
			"--version", bot.Spec.MinecraftVersion,
			"--dest", assetDir(bot, assets),
		},
		Env: []corev1.EnvVar{
			{Name: "MC_VERSION", Value: bot.Spec.MinecraftVersion},
			{Name: "MC_ASSETS_DIR", Value: assetDir(bot, assets)},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: volumeAssets, MountPath: assets.MountPath},
			{Name: volumeTmp, MountPath: "/tmp"},
		},
		Resources: assets.Resources,
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptr(false),
			ReadOnlyRootFilesystem:   ptr(true),
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		},
	}
}

func containerEnv(bot *v1alpha1.MinecraftBot) []corev1.EnvVar {
	env := []corev1.EnvVar{
		{Name: "MCP_HOST", Value: bot.Spec.Server.Host},
		{Name: "MCP_PORT", Value: strconv.Itoa(int(port(bot)))},
		{Name: "BOT_NAME", Value: BotName(bot)},
		{Name: "BOT_KIND", Value: string(bot.Spec.Kind)},
		{Name: "BOT_HEALTH_PORT", Value: strconv.Itoa(HealthPort)},
		{Name: "BOT_WORK_DIR", Value: WorkDir},
		{Name: "MC_VERSION", Value: bot.Spec.MinecraftVersion},
		{Name: "POD_NAME", ValueFrom: fieldRef("metadata.name")},
		{Name: "POD_NAMESPACE", ValueFrom: fieldRef("metadata.namespace")},
		{Name: "NODE_NAME", ValueFrom: fieldRef("spec.nodeName")},
	}
	if bot.Spec.Kind == v1alpha1.BotKindFabric {
		assets := assetSpec(bot)
		render := renderSpec(bot)
		env = append(env,
			corev1.EnvVar{Name: "MC_ASSETS_DIR", Value: assetDir(bot, assets)},
			corev1.EnvVar{Name: "BOT_RENDER", Value: strconv.FormatBool(render.Enabled == nil || *render.Enabled)},
			corev1.EnvVar{Name: "BOT_RENDER_WIDTH", Value: strconv.Itoa(int(render.Width))},
			corev1.EnvVar{Name: "BOT_RENDER_HEIGHT", Value: strconv.Itoa(int(render.Height))},
			corev1.EnvVar{Name: "BOT_FRAME_RATE_LIMIT", Value: strconv.Itoa(int(render.FrameRateLimit))},
		)
	}
	return append(env, bot.Spec.Env...)
}

func assetSpec(bot *v1alpha1.MinecraftBot) *v1alpha1.AssetCacheSpec {
	assets := bot.Spec.Assets
	if assets == nil {
		assets = &v1alpha1.AssetCacheSpec{}
	} else {
		assets = assets.DeepCopy()
	}
	if assets.MountPath == "" {
		assets.MountPath = DefaultAssetsMount
	}
	return assets
}

func renderSpec(bot *v1alpha1.MinecraftBot) *v1alpha1.RenderSpec {
	render := bot.Spec.Render
	if render == nil {
		render = &v1alpha1.RenderSpec{}
	} else {
		render = render.DeepCopy()
	}
	if render.Width == 0 {
		render.Width = 854
	}
	if render.Height == 0 {
		render.Height = 480
	}
	if render.FrameRateLimit == 0 {
		render.FrameRateLimit = 1
	}
	return render
}

func assetDir(bot *v1alpha1.MinecraftBot, assets *v1alpha1.AssetCacheSpec) string {
	return path.Join(assets.MountPath, bot.Spec.MinecraftVersion)
}

func assetVolume(assets *v1alpha1.AssetCacheSpec) corev1.Volume {
	if assets.ClaimName == "" {
		return emptyDir(volumeAssets, assets.SizeLimit)
	}
	return corev1.Volume{
		Name: volumeAssets,
		VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
				ClaimName: assets.ClaimName,
				ReadOnly:  assets.ReadOnly && assets.Prefilled,
			},
		},
	}
}

func BotName(bot *v1alpha1.MinecraftBot) string {
	if bot.Spec.BotName != "" {
		return bot.Spec.BotName
	}
	name := bot.Name
	if len(name) > 16 {
		name = name[:16]
	}
	return name
}

func podLabels(bot *v1alpha1.MinecraftBot) map[string]string {
	labels := map[string]string{}
	maps.Copy(labels, bot.Spec.PodLabels)
	labels[v1alpha1.LabelName] = v1alpha1.BotPodName
	labels[v1alpha1.LabelManagedBy] = v1alpha1.ManagedByValue
	labels[v1alpha1.LabelBot] = bot.Name
	labels[v1alpha1.LabelBotKind] = string(bot.Spec.Kind)
	if pool, ok := bot.Labels[v1alpha1.LabelPool]; ok {
		labels[v1alpha1.LabelPool] = pool
	}
	return labels
}

func podAnnotations(bot *v1alpha1.MinecraftBot) map[string]string {
	annotations := map[string]string{}
	maps.Copy(annotations, bot.Spec.PodAnnotations)
	return annotations
}

func resources(bot *v1alpha1.MinecraftBot) corev1.ResourceRequirements {
	if len(bot.Spec.Resources.Requests) > 0 || len(bot.Spec.Resources.Limits) > 0 {
		return bot.Spec.Resources
	}
	if bot.Spec.Kind == v1alpha1.BotKindFabric {
		return corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("500m"),
				corev1.ResourceMemory: resource.MustParse("1536Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("2Gi"),
			},
		}
	}
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("100m"),
			corev1.ResourceMemory: resource.MustParse("192Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("384Mi"),
		},
	}
}

func startupProbe(kind v1alpha1.BotKind) *corev1.Probe {
	threshold := int32(60)
	if kind == v1alpha1.BotKindFabric {
		threshold = 180
	}
	return &corev1.Probe{
		ProbeHandler:     httpGet("/healthz"),
		PeriodSeconds:    2,
		FailureThreshold: threshold,
	}
}

func readinessProbe() *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler:     httpGet("/readyz"),
		PeriodSeconds:    5,
		FailureThreshold: 3,
	}
}

func livenessProbe() *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler:     httpGet("/healthz"),
		PeriodSeconds:    30,
		TimeoutSeconds:   5,
		FailureThreshold: 3,
	}
}

func httpGet(path string) corev1.ProbeHandler {
	return corev1.ProbeHandler{
		HTTPGet: &corev1.HTTPGetAction{
			Path: path,
			Port: intstr.FromString(HealthPortName),
		},
	}
}

func emptyDir(name string, sizeLimit *resource.Quantity) corev1.Volume {
	return corev1.Volume{
		Name:         name,
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: sizeLimit}},
	}
}

func fieldRef(field string) *corev1.EnvVarSource {
	return &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: field}}
}

func gracePeriod(bot *v1alpha1.MinecraftBot) *int64 {
	if bot.Spec.TerminationGracePeriodSeconds != nil {
		return bot.Spec.TerminationGracePeriodSeconds
	}
	return ptr(int64(defaultGracePeriod))
}

func port(bot *v1alpha1.MinecraftBot) int32 {
	if bot.Spec.Server.Port != 0 {
		return bot.Spec.Server.Port
	}
	return v1alpha1.DefaultMCPPort
}

func ptr[T any](v T) *T { return &v }
