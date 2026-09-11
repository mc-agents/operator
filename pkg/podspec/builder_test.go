package podspec_test

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mc-agents/operator/api/v1alpha1"
	"github.com/mc-agents/operator/pkg/botimage"
	"github.com/mc-agents/operator/pkg/podspec"
)

func newBuilder() podspec.Builder {
	return podspec.NewBuilder(
		botimage.NewResolver("ghcr.io/mc-agents", botimage.Tags{Mineflayer: "0.5.0", Fabric: "0.2.0"}),
		podspec.Defaults{AssetFetcherImage: "ghcr.io/mc-agents/mc-assets:0.1.0"},
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

func TestMineflayerPodHasNoAssetPlumbing(t *testing.T) {
	pod, err := newBuilder().Build(newBot(v1alpha1.BotKindMineflayer))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(pod.Spec.InitContainers) != 0 {
		t.Fatalf("mineflayer pods must not fetch client assets, got %d init containers", len(pod.Spec.InitContainers))
	}
	if volume(pod, "assets") != nil {
		t.Fatal("mineflayer pods must not carry an assets volume")
	}
	if env(pod, "MC_ASSETS_DIR") != "" {
		t.Fatal("mineflayer pods must not advertise an assets directory")
	}
}

func TestFabricPodFetchesAssetsIntoAVersionedDirectory(t *testing.T) {
	bot := newBot(v1alpha1.BotKindFabric)
	pod, err := newBuilder().Build(bot)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(pod.Spec.InitContainers) != 1 {
		t.Fatalf("want one init container, got %d", len(pod.Spec.InitContainers))
	}
	init := pod.Spec.InitContainers[0]
	if init.Image != "ghcr.io/mc-agents/mc-assets:0.1.0" {
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
	pod, err := newBuilder().Build(bot)
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
	pod, err := newBuilder().Build(newBot(v1alpha1.BotKindMineflayer))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for key, want := range map[string]string{
		"MCP_HOST": "mcp.qa.svc",
		"MCP_PORT": "8765",
		"BOT_NAME": "scout",
		"BOT_KIND": "mineflayer",
	} {
		if got := env(pod, key); got != want {
			t.Errorf("%s is %q, want %q", key, got, want)
		}
	}
	if pod.Spec.EnableServiceLinks == nil || *pod.Spec.EnableServiceLinks {
		t.Error("service link env vars would shadow MCP_PORT, so they must be off")
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

	base, err := builder.Build(newBot(v1alpha1.BotKindMineflayer))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	same, err := builder.Build(newBot(v1alpha1.BotKindMineflayer))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if base.Annotations[v1alpha1.AnnotationSpecHash] != same.Annotations[v1alpha1.AnnotationSpecHash] {
		t.Fatal("the same spec must hash the same, or every reconcile would recreate the pod")
	}

	changed := newBot(v1alpha1.BotKindMineflayer)
	changed.Spec.Server.Host = "elsewhere.qa.svc"
	moved, err := builder.Build(changed)
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
