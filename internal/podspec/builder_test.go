package podspec_test

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mc-agents/operator/api/v1alpha1"
	"github.com/mc-agents/operator/internal/botimage"
	"github.com/mc-agents/operator/internal/podspec"
	"github.com/mc-agents/operator/internal/profile"
)

func newBuilder() podspec.Builder {
	return podspec.NewBuilder(
		botimage.NewResolver("junhyung.cloud/library", botimage.Tags{Fabric: "0.2.0", Azalea: "0.4.0"}),
		podspec.Defaults{AssetFetcherImage: "junhyung.cloud/library/mc-assets:0.1.0"},
	)
}

func newBot(kind v1alpha1.BotKind) *v1alpha1.MinecraftBot {
	return &v1alpha1.MinecraftBot{
		ObjectMeta: metav1.ObjectMeta{Name: "scout", Namespace: "qa", UID: "bot-uid"},
		Spec: v1alpha1.MinecraftBotSpec{
			Kind:             kind,
			MinecraftVersion: "26.1.2",
			Server:           v1alpha1.MCPServerRef{Host: "mcp.qa.svc", Port: 8765},
		},
	}
}

func TestAzaleaPodHasNoAssetPlumbing(t *testing.T) {
	pod, err := newBuilder().Build(newBot(v1alpha1.BotKindAzalea), profile.Resolved{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(pod.Spec.InitContainers) != 0 {
		t.Fatalf("azalea pods must not fetch client assets, got %d init containers", len(pod.Spec.InitContainers))
	}
	if volume(pod, "assets") != nil {
		t.Fatal("azalea pods must not carry an assets volume")
	}
	if env(pod, "MC_ASSETS_DIR") != "" {
		t.Fatal("azalea pods must not advertise an assets directory")
	}
}

func TestFabricPodFetchesAssetsIntoAVersionedDirectory(t *testing.T) {
	bot := newBot(v1alpha1.BotKindFabric)
	pod, err := newBuilder().Build(bot, profile.Resolved{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(pod.Spec.InitContainers) != 1 {
		t.Fatalf("want one init container, got %d", len(pod.Spec.InitContainers))
	}
	init := pod.Spec.InitContainers[0]
	if init.Image != "junhyung.cloud/library/mc-assets:0.1.0-mc26.1.2" {
		t.Fatalf("init container image is %q", init.Image)
	}
	if got, want := env(pod, "MC_ASSETS_DIR"), "/mc-assets/26.1.2"; got != want {
		t.Fatalf("MC_ASSETS_DIR is %q, want %q", got, want)
	}
	if volume(pod, "assets") == nil {
		t.Fatal("fabric pods need an assets volume")
	}
}

func TestPrefilledAssetsSkipTheFetcher(t *testing.T) {
	bot := newBot(v1alpha1.BotKindFabric)
	bot.Spec.Assets = &v1alpha1.AssetCacheSpec{
		ClaimName: "mc-assets-26-1-2",
		Prefilled: true,
		ReadOnly:  true,
	}
	pod, err := newBuilder().Build(bot, profile.Resolved{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(pod.Spec.InitContainers) != 0 {
		t.Fatal("a prefilled cache must not run the fetcher")
	}
	assets := volume(pod, "assets")
	if assets == nil || assets.PersistentVolumeClaim == nil {
		t.Fatal("want a PVC-backed assets volume")
	}
	if !assets.PersistentVolumeClaim.ReadOnly {
		t.Fatal("a prefilled read-only cache must be mounted read-only")
	}
}

func TestPodCarriesTheLinkContract(t *testing.T) {
	pod, err := newBuilder().Build(newBot(v1alpha1.BotKindFabric), profile.Resolved{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for key, want := range map[string]string{
		"MCP_SERVER_HOST": "mcp.qa.svc",
		"MCP_SERVER_PORT": "8765",
		"BOT_NAME":        "scout",
		"BOT_KIND":        "fabric",
	} {
		if got := env(pod, key); got != want {
			t.Errorf("%s is %q, want %q", key, got, want)
		}
	}
	if pod.Spec.EnableServiceLinks == nil || *pod.Spec.EnableServiceLinks {
		t.Error("service link env vars would shadow MCP_SERVER_PORT, so they must be off")
	}
	if pod.Spec.RestartPolicy != corev1.RestartPolicyAlways {
		t.Errorf("restart policy is %q; a crashed bot has to come back without the operator", pod.Spec.RestartPolicy)
	}
	if len(pod.OwnerReferences) != 1 || pod.OwnerReferences[0].UID != "bot-uid" {
		t.Error("the pod must be owned by its MinecraftBot so deleting the CR collects it")
	}
}

func TestSpecHashTracksTheSpec(t *testing.T) {
	builder := newBuilder()

	base, err := builder.Build(newBot(v1alpha1.BotKindFabric), profile.Resolved{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	same, err := builder.Build(newBot(v1alpha1.BotKindFabric), profile.Resolved{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if base.Annotations[v1alpha1.AnnotationSpecHash] != same.Annotations[v1alpha1.AnnotationSpecHash] {
		t.Fatal("the same spec must hash the same, or every reconcile would recreate the pod")
	}

	changed := newBot(v1alpha1.BotKindFabric)
	changed.Spec.Server.Host = "elsewhere.qa.svc"
	moved, err := builder.Build(changed, profile.Resolved{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if base.Annotations[v1alpha1.AnnotationSpecHash] == moved.Annotations[v1alpha1.AnnotationSpecHash] {
		t.Fatal("a different MCP server must change the hash, or the pod would keep dialling the old one")
	}
}

func env(pod *corev1.Pod, name string) string {
	for _, e := range pod.Spec.Containers[0].Env {
		if e.Name == name {
			return e.Value
		}
	}
	return ""
}

func volume(pod *corev1.Pod, name string) *corev1.VolumeSource {
	for i := range pod.Spec.Volumes {
		if pod.Spec.Volumes[i].Name == name {
			return &pod.Spec.Volumes[i].VolumeSource
		}
	}
	return nil
}

/*
A fabric bot's image used to be linux/amd64 only, and the operator pinned it to an amd64 node so
that the pod did not land somewhere containerd would refuse the manifest. bot-fabric substitutes
LWJGL's own arm64 natives now and the image is multi-architecture, so nothing here chooses a node
for anybody.
*/
func TestNothingIsPinnedToAnArchitecture(t *testing.T) {
	for _, kind := range []v1alpha1.BotKind{v1alpha1.BotKindFabric, v1alpha1.BotKindAzalea} {
		pod, err := newBuilder().Build(newBot(kind), profile.Resolved{})
		if err != nil {
			t.Fatalf("Build(%s): %v", kind, err)
		}
		if _, pinned := pod.Spec.NodeSelector[corev1.LabelArchStable]; pinned {
			t.Errorf("a %s bot asks for an architecture it no longer needs", kind)
		}
	}
}

/* Someone who says where a bot goes means it. */
func TestAnExplicitNodeSelectorIsHonoured(t *testing.T) {
	bot := newBot(v1alpha1.BotKindFabric)
	bot.Spec.NodeSelector = map[string]string{"pool": "renderers"}

	pod, err := newBuilder().Build(bot, profile.Resolved{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if pod.Spec.NodeSelector["pool"] != "renderers" {
		t.Error("spec.nodeSelector was not passed through")
	}
}

/*
The asset fetcher is published per Minecraft version, like a fabric bot's image and for the same
reason: it bakes the Fabric loader that bot was built against. The chart's default named a plain
"latest" tag that has never existed in the registry.
*/
func TestTheAssetFetcherIsPickedForTheMinecraftVersion(t *testing.T) {
	pod, err := newBuilder().Build(newBot(v1alpha1.BotKindFabric), profile.Resolved{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(pod.Spec.InitContainers) != 1 {
		t.Fatalf("a fabric bot has %d init containers, want 1", len(pod.Spec.InitContainers))
	}
	if got := pod.Spec.InitContainers[0].Image; !strings.HasSuffix(got, "-mc26.1.2") {
		t.Errorf("the fetcher image is %q, which names no Minecraft version", got)
	}
}

/* A tag that already names one, or a digest, is what the caller meant. */
func TestAnExplicitFetcherImageIsLeftAlone(t *testing.T) {
	for _, image := range []string{
		"example.test/mc-assets:0.6.0-mc26.1.2",
		"example.test/mc-assets@sha256:" + strings.Repeat("0", 64),
	} {
		bot := newBot(v1alpha1.BotKindFabric)
		bot.Spec.Assets = &v1alpha1.AssetCacheSpec{FetcherImage: image}

		pod, err := newBuilder().Build(bot, profile.Resolved{})
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if got := pod.Spec.InitContainers[0].Image; got != image {
			t.Errorf("the fetcher image became %q, want %q", got, image)
		}
	}
}

func TestBuildFillsWhatTheBotLeavesEmptyFromTheProfile(t *testing.T) {
	bot := newBot(v1alpha1.BotKindAzalea)
	bot.Spec.PodLabels = map[string]string{"team": "qa"}
	resolved := profile.Resolved{Spec: v1alpha1.BotProfileSpec{
		Azalea: v1alpha1.AzaleaProfile{
			Tag: "0.16.0",
			Resources: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512Mi")},
			},
		},
		NodeSelector: map[string]string{"pool": "bots"},
		PodLabels:    map[string]string{"team": "platform", "cost": "bots"},
	}}

	pod, err := newBuilder().Build(bot, resolved)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	c := pod.Spec.Containers[0]
	if want := "junhyung.cloud/library/bot-azalea:0.16.0-mc26.1.2"; c.Image != want {
		t.Fatalf("image got %q, want %q", c.Image, want)
	}
	if got := c.Resources.Limits.Memory().String(); got != "512Mi" {
		t.Fatalf("memory limit got %s, want the profile's 512Mi", got)
	}
	if pod.Spec.NodeSelector["pool"] != "bots" {
		t.Fatalf("node selector %v did not come from the profile", pod.Spec.NodeSelector)
	}
	if pod.Labels["team"] != "qa" || pod.Labels["cost"] != "bots" {
		t.Fatalf("labels %v: the bot's own key should win and the profile's others should be added", pod.Labels)
	}
	if bot.Spec.NodeSelector != nil {
		t.Fatal("Build wrote the profile into the bot it was given")
	}
}
