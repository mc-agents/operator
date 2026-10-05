package podspec_test

import (
	"os"
	"strconv"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/mc-agents/operator/api/v1alpha1"
	"github.com/mc-agents/operator/internal/botimage"
	"github.com/mc-agents/operator/internal/podspec"
	"github.com/mc-agents/operator/internal/profile"
)

func newBuilder() podspec.Builder {
	return podspec.NewBuilder(
		botimage.NewResolver("junhyung.cloud/mc-agents", botimage.Tags{Fabric: "0.2.0", Azalea: "0.4.0"}),
		podspec.Defaults{AssetFetcherImage: "junhyung.cloud/mc-agents/mc-assets:0.1.0"},
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
	if init.Image != "junhyung.cloud/mc-agents/mc-assets:0.1.0-mc26.1.2" {
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

func TestTheLinkTokenComesFromTheSecretTheBotNames(t *testing.T) {
	builder := newBuilder()

	without, err := builder.Build(newBot(v1alpha1.BotKindAzalea), profile.Resolved{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if envSource(without, "BOT_LINK_TOKEN") != nil || env(without, "BOT_LINK_TOKEN") != "" {
		t.Fatal("a bot naming no Secret must be handed no BOT_LINK_TOKEN; the server ignores a missing one")
	}

	bot := newBot(v1alpha1.BotKindAzalea)
	bot.Spec.LinkTokenSecretRef = &v1alpha1.LinkTokenSecretRef{Name: "mc-agents-mcp-server-link"}
	pod, err := builder.Build(bot, profile.Resolved{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	source := envSource(pod, "BOT_LINK_TOKEN")
	if source == nil || source.SecretKeyRef == nil {
		t.Fatalf("BOT_LINK_TOKEN is not read from a Secret: %+v", source)
	}
	// The value stays in the Secret: a pod is what people paste when a bot will not link.
	if ref := source.SecretKeyRef; ref.Name != "mc-agents-mcp-server-link" || ref.Key != "token" {
		t.Errorf("BOT_LINK_TOKEN comes from %s/%s, want mc-agents-mcp-server-link/token (the key defaulted)", ref.Name, ref.Key)
	}
	if without.Annotations[v1alpha1.AnnotationSpecHash] == pod.Annotations[v1alpha1.AnnotationSpecHash] {
		t.Error("giving a running bot a link token must replace its pod, or it never presents one")
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

func envSource(pod *corev1.Pod, name string) *corev1.EnvVarSource {
	for _, e := range pod.Spec.Containers[0].Env {
		if e.Name == name {
			return e.ValueFrom
		}
	}
	return nil
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
	if want := "junhyung.cloud/mc-agents/bot-azalea:0.16.0-mc26.1.2"; c.Image != want {
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

// Every render setting used to stop at the pod: the operator wrote BOT_RENDER, BOT_RENDER_WIDTH
// and BOT_RENDER_HEIGHT, which no bot has ever read, while the two names a fabric bot does read --
// BOT_RENDER_DISTANCE and BOT_SCREEN -- were not sent at all. Nothing failed, because nothing
// here looked. These tests are that look.
func TestFabricRenderSettingsReachTheClient(t *testing.T) {
	bot := newBot(v1alpha1.BotKindFabric)
	bot.Spec.Render = &v1alpha1.RenderSpec{Width: 1920, Height: 1080, Distance: 4, FrameRateLimit: 30}

	pod, err := newBuilder().Build(bot, profile.Resolved{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	for name, want := range map[string]string{
		"BOT_SCREEN":           "1920x1080x24",
		"BOT_RENDER_DISTANCE":  "4",
		"BOT_FRAME_RATE_LIMIT": "30",
	} {
		if got := env(pod, name); got != want {
			t.Errorf("%s is %q, want %q", name, got, want)
		}
	}
	// The window, which is the resolution a frame is captured at: the client defaults to 854x480
	// and the entrypoint passes the container's arguments on to it. A screenshot comes back at the
	// size its caller asked for either way, so this is the detail in it and not its dimensions.
	if got, want := strings.Join(pod.Spec.Containers[0].Args, " "), "--width 1920 --height 1080"; got != want {
		t.Errorf("bot container args are %q, want %q", got, want)
	}
}

// The CRD's defaults and the builder's fallbacks are two copies of four numbers, and only one of
// them is reachable from Go. The API server fills a field absent from a render block it can see and
// does not invent the block, so a bot with no render at all reaches the builder empty and the
// fallbacks decide -- which means the two copies answer the same question and nothing but this
// compares them. Read the schema rather than restating it, or the test is the third copy.
func TestFabricRenderDefaultsAreTheOnesTheCRDDeclares(t *testing.T) {
	declared := renderDefaults(t)

	pod, err := newBuilder().Build(newBot(v1alpha1.BotKindFabric), profile.Resolved{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	for name, field := range map[string]string{
		"BOT_RENDER_DISTANCE":  "distance",
		"BOT_FRAME_RATE_LIMIT": "frameRateLimit",
	} {
		if got, want := env(pod, name), declared[field]; got != want {
			t.Errorf("%s is %q, but the CRD defaults render.%s to %q", name, got, field, want)
		}
	}
	if got, want := env(pod, "BOT_SCREEN"), declared["width"]+"x"+declared["height"]+"x24"; got != want {
		t.Errorf("BOT_SCREEN is %q, want %q from the CRD's width and height", got, want)
	}
	if got, want := strings.Join(pod.Spec.Containers[0].Args, " "),
		"--width "+declared["width"]+" --height "+declared["height"]; got != want {
		t.Errorf("bot container args are %q, want %q", got, want)
	}
}

// render's defaults out of the MinecraftBot CRD the chart installs, as decimal strings.
func renderDefaults(t *testing.T) map[string]string {
	t.Helper()

	const crd = "../../charts/mc-agents-operator/files/crds/mc-agents.junhyung.cloud_minecraftbots.yaml"
	raw, err := os.ReadFile(crd)
	if err != nil {
		t.Fatalf("reading the generated CRD: %v", err)
	}

	var parsed struct {
		Spec struct {
			Versions []struct {
				Schema struct {
					OpenAPIV3Schema struct {
						Properties struct {
							Spec struct {
								Properties struct {
									Render struct {
										Properties map[string]struct {
											Default *int64 `json:"default"`
										} `json:"properties"`
									} `json:"render"`
								} `json:"properties"`
							} `json:"spec"`
						} `json:"properties"`
					} `json:"openAPIV3Schema"`
				} `json:"schema"`
			} `json:"versions"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("parsing the generated CRD: %v", err)
	}
	if len(parsed.Spec.Versions) != 1 {
		t.Fatalf("want one served version, got %d", len(parsed.Spec.Versions))
	}

	fields := parsed.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties.Spec.Properties.Render.Properties
	if len(fields) == 0 {
		t.Fatal("the CRD declares no render properties, so this test is comparing nothing")
	}
	defaults := make(map[string]string, len(fields))
	for name, field := range fields {
		if field.Default == nil {
			t.Errorf("render.%s has no default in the CRD, so a bot that omits it gets a zero", name)
			continue
		}
		defaults[name] = strconv.FormatInt(*field.Default, 10)
	}
	return defaults
}

func TestTheVirtualDisplayIsTheSizeOfTheWindow(t *testing.T) {
	bot := newBot(v1alpha1.BotKindFabric)
	bot.Spec.Render = &v1alpha1.RenderSpec{Width: 1280, Height: 720}

	pod, err := newBuilder().Build(bot, profile.Resolved{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// A window larger than the X root is a clipped window, and the two are set from one pair of
	// fields so they cannot drift apart.
	screen := env(pod, "BOT_SCREEN")
	args := strings.Join(pod.Spec.Containers[0].Args, " ")
	if screen != "1280x720x24" || args != "--width 1280 --height 720" {
		t.Fatalf("display %q and window %q disagree", screen, args)
	}
}

func TestAzaleaPodHasNoRenderPlumbing(t *testing.T) {
	bot := newBot(v1alpha1.BotKindAzalea)

	pod, err := newBuilder().Build(bot, profile.Resolved{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// A headless bot reads none of these, and the CRD refuses a render block on one. Sending them
	// anyway would be a pod that restarts when a setting it cannot use changes.
	for _, name := range []string{"BOT_SCREEN", "BOT_RENDER_DISTANCE", "BOT_FRAME_RATE_LIMIT"} {
		if got := env(pod, name); got != "" {
			t.Errorf("azalea pods must not carry %s, got %q", name, got)
		}
	}
	if len(pod.Spec.Containers[0].Args) != 0 {
		t.Errorf("azalea bots take no arguments, got %v", pod.Spec.Containers[0].Args)
	}
}

func TestRenderChangesReplaceThePod(t *testing.T) {
	builder := newBuilder()

	bot := newBot(v1alpha1.BotKindFabric)
	bot.Spec.Render = &v1alpha1.RenderSpec{Width: 854, Height: 480, Distance: 8, FrameRateLimit: 1}
	before, err := builder.Build(bot, profile.Resolved{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Writing the defaults out by hand has to hash the same as leaving the block off, or a bot
	// mcp-server created and a bot somebody wrote would fight over the same pod.
	plain, err := builder.Build(newBot(v1alpha1.BotKindFabric), profile.Resolved{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if before.Annotations[v1alpha1.AnnotationSpecHash] != plain.Annotations[v1alpha1.AnnotationSpecHash] {
		t.Fatal("the written-out defaults must hash the same as no render block at all")
	}

	bot.Spec.Render.Distance = 16
	further, err := builder.Build(bot, profile.Resolved{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if before.Annotations[v1alpha1.AnnotationSpecHash] == further.Annotations[v1alpha1.AnnotationSpecHash] {
		t.Fatal("a render distance change must change the hash, or the pod would keep the old one")
	}
}

// The profile is where a cluster sets a render distance for every bot it has, because the bots
// mcp-server creates have no render block of their own and never will. Both the environment and the
// arguments are built from the bot after the profile has been folded into it, and they are built by
// two different functions, so a profile that reached one and not the other would be a pod whose
// window and whose display disagreed.
func TestAProfilesRenderReachesBothTheEnvironmentAndTheArguments(t *testing.T) {
	resolved := profile.Resolved{Spec: v1alpha1.BotProfileSpec{
		Fabric: v1alpha1.FabricProfile{
			Render: &v1alpha1.RenderSpec{Width: 1920, Height: 1080, Distance: 2, FrameRateLimit: 5},
		},
	}}

	pod, err := newBuilder().Build(newBot(v1alpha1.BotKindFabric), resolved)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if got, want := env(pod, "BOT_SCREEN"), "1920x1080x24"; got != want {
		t.Errorf("BOT_SCREEN is %q, want %q", got, want)
	}
	if got, want := env(pod, "BOT_RENDER_DISTANCE"), "2"; got != want {
		t.Errorf("BOT_RENDER_DISTANCE is %q, want %q", got, want)
	}
	if got, want := env(pod, "BOT_FRAME_RATE_LIMIT"), "5"; got != want {
		t.Errorf("BOT_FRAME_RATE_LIMIT is %q, want %q", got, want)
	}
	if got, want := strings.Join(pod.Spec.Containers[0].Args, " "), "--width 1920 --height 1080"; got != want {
		t.Errorf("bot container args are %q, want %q", got, want)
	}
}

// And the other half of that: the profile supplies the block whole or not at all, so a bot that
// writes one field writes all four. Worth a test because it is a trap rather than a convenience --
// a bot raising its frame rate silently loses the distance its namespace chose for it.
func TestABotsOwnRenderBlockReplacesTheProfilesEntirely(t *testing.T) {
	resolved := profile.Resolved{Spec: v1alpha1.BotProfileSpec{
		Fabric: v1alpha1.FabricProfile{
			Render: &v1alpha1.RenderSpec{Width: 1920, Height: 1080, Distance: 2, FrameRateLimit: 1},
		},
	}}

	bot := newBot(v1alpha1.BotKindFabric)
	bot.Spec.Render = &v1alpha1.RenderSpec{FrameRateLimit: 30}

	pod, err := newBuilder().Build(bot, resolved)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if got, want := env(pod, "BOT_FRAME_RATE_LIMIT"), "30"; got != want {
		t.Errorf("BOT_FRAME_RATE_LIMIT is %q, want %q", got, want)
	}
	// Not the profile's 2: the bot's block won, and the rest of it came from the defaults.
	if got, want := env(pod, "BOT_RENDER_DISTANCE"), "8"; got != want {
		t.Errorf("BOT_RENDER_DISTANCE is %q, want %q -- the profile's render must not be merged field by field", got, want)
	}
	if got, want := env(pod, "BOT_SCREEN"), "854x480x24"; got != want {
		t.Errorf("BOT_SCREEN is %q, want %q", got, want)
	}
}
