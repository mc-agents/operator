package botimage_test

import (
	"testing"

	"github.com/mc-agents/operator/api/v1alpha1"
	"github.com/mc-agents/operator/internal/botimage"
)

func TestResolve(t *testing.T) {
	resolver := botimage.NewResolver("junhyung.cloud/library", botimage.Tags{
		Mineflayer: "0.5.0",
		Fabric:     "0.2.0",
	})

	cases := []struct {
		name string
		spec v1alpha1.MinecraftBotSpec
		want string
	}{
		{
			name: "mineflayer ignores the minecraft version",
			spec: v1alpha1.MinecraftBotSpec{Kind: v1alpha1.BotKindMineflayer, MinecraftVersion: "26.1.2"},
			want: "junhyung.cloud/library/bot-mineflayer:0.5.0",
		},
		{
			name: "fabric pins the minecraft version in the tag",
			spec: v1alpha1.MinecraftBotSpec{Kind: v1alpha1.BotKindFabric, MinecraftVersion: "26.1.2"},
			want: "junhyung.cloud/library/bot-fabric:0.2.0-mc26.1.2",
		},
		{
			name: "an explicit tag wins over the version suffix",
			spec: v1alpha1.MinecraftBotSpec{
				Kind:             v1alpha1.BotKindFabric,
				MinecraftVersion: "26.1.2",
				Image:            v1alpha1.ImageOverride{Tag: "pr-42"},
			},
			want: "junhyung.cloud/library/bot-fabric:pr-42",
		},
		{
			name: "a repository that already carries a tag is left alone",
			spec: v1alpha1.MinecraftBotSpec{
				Kind:             v1alpha1.BotKindFabric,
				MinecraftVersion: "26.1.2",
				Image:            v1alpha1.ImageOverride{Repository: "localhost:5000/bot-fabric:dev"},
			},
			want: "localhost:5000/bot-fabric:dev",
		},
		{
			name: "a digest reference survives untouched",
			spec: v1alpha1.MinecraftBotSpec{
				Kind:             v1alpha1.BotKindMineflayer,
				MinecraftVersion: "26.1.2",
				Image: v1alpha1.ImageOverride{
					Repository: "junhyung.cloud/library/bot-mineflayer@sha256:" + zeros(64),
				},
			},
			want: "junhyung.cloud/library/bot-mineflayer@sha256:" + zeros(64),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolver.Resolve(&tc.spec)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveRejectsUnknownKind(t *testing.T) {
	resolver := botimage.NewResolver("", botimage.Tags{Mineflayer: "1", Fabric: "1"})
	if _, err := resolver.Resolve(&v1alpha1.MinecraftBotSpec{Kind: "quake"}); err == nil {
		t.Fatal("expected an error for an unknown bot kind")
	}
}

func TestResolveRejectsFabricWithoutVersion(t *testing.T) {
	resolver := botimage.NewResolver("", botimage.Tags{Fabric: "1"})
	_, err := resolver.Resolve(&v1alpha1.MinecraftBotSpec{Kind: v1alpha1.BotKindFabric})
	if err == nil {
		t.Fatal("expected an error when the minecraft version is missing")
	}
}

func zeros(n int) string {
	out := make([]byte, n)
	for i := range out {
		out[i] = '0'
	}
	return string(out)
}
