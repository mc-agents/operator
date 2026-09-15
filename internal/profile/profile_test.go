package profile_test

import (
	"context"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/mc-agents/operator/api/v1alpha1"
	"github.com/mc-agents/operator/internal/profile"
)

func reader(t *testing.T, objects ...client.Object) client.Reader {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}

func namespaced(name string, spec v1alpha1.BotProfileSpec) *v1alpha1.MinecraftBotProfile {
	return &v1alpha1.MinecraftBotProfile{ObjectMeta: metav1.ObjectMeta{Namespace: "game", Name: name}, Spec: spec}
}

func cluster(name string, spec v1alpha1.BotProfileSpec) *v1alpha1.ClusterMinecraftBotProfile {
	return &v1alpha1.ClusterMinecraftBotProfile{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: spec}
}

func bot(ref *v1alpha1.ProfileRef) *v1alpha1.MinecraftBot {
	return &v1alpha1.MinecraftBot{
		ObjectMeta: metav1.ObjectMeta{Namespace: "game", Name: "scout"},
		Spec:       v1alpha1.MinecraftBotSpec{Kind: v1alpha1.BotKindAzalea, ProfileRef: ref},
	}
}

func TestLookup(t *testing.T) {
	objects := []client.Object{
		cluster("default", v1alpha1.BotProfileSpec{
			Registry: "cluster.registry",
			Fabric:   v1alpha1.FabricProfile{Tag: "0.60.0"},
			Azalea:   v1alpha1.AzaleaProfile{Tag: "0.10.0"},
		}),
		cluster("canary", v1alpha1.BotProfileSpec{Azalea: v1alpha1.AzaleaProfile{Tag: "0.17.0-rc"}}),
		namespaced("default", v1alpha1.BotProfileSpec{Azalea: v1alpha1.AzaleaProfile{Tag: "0.16.0"}}),
		namespaced("slow", v1alpha1.BotProfileSpec{Fabric: v1alpha1.FabricProfile{Tag: "0.63.0"}}),
	}

	cases := []struct {
		name        string
		objects     []client.Object
		ref         *v1alpha1.ProfileRef
		wantSources []string
		wantAzalea  string
		wantFabric  string
		wantReg     string
	}{
		{
			name:        "no reference takes the namespace default over the cluster default",
			objects:     objects,
			wantSources: []string{"MinecraftBotProfile/default", "ClusterMinecraftBotProfile/default"},
			wantAzalea:  "0.16.0",
			wantFabric:  "0.60.0",
			wantReg:     "cluster.registry",
		},
		{
			name:        "a named profile replaces the namespace default",
			objects:     objects,
			ref:         &v1alpha1.ProfileRef{Name: "slow"},
			wantSources: []string{"MinecraftBotProfile/slow", "ClusterMinecraftBotProfile/default"},
			wantAzalea:  "0.10.0",
			wantFabric:  "0.63.0",
			wantReg:     "cluster.registry",
		},
		{
			name:        "a named cluster profile sits over the cluster default",
			objects:     objects,
			ref:         &v1alpha1.ProfileRef{Kind: v1alpha1.ProfileKindCluster, Name: "canary"},
			wantSources: []string{"ClusterMinecraftBotProfile/canary", "ClusterMinecraftBotProfile/default"},
			wantAzalea:  "0.17.0-rc",
			wantFabric:  "0.60.0",
			wantReg:     "cluster.registry",
		},
		{
			name:        "naming the cluster default reads it once",
			objects:     objects,
			ref:         &v1alpha1.ProfileRef{Kind: v1alpha1.ProfileKindCluster, Name: "default"},
			wantSources: []string{"ClusterMinecraftBotProfile/default"},
			wantAzalea:  "0.10.0",
			wantFabric:  "0.60.0",
			wantReg:     "cluster.registry",
		},
		{
			name: "no profiles at all is no error",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := profile.Lookup(context.Background(), reader(t, tc.objects...), bot(tc.ref))
			if err != nil {
				t.Fatalf("Lookup: %v", err)
			}
			if !slices.Equal(got.Sources, tc.wantSources) {
				t.Fatalf("sources got %v, want %v", got.Sources, tc.wantSources)
			}
			if got.Spec.Azalea.Tag != tc.wantAzalea || got.Spec.Fabric.Tag != tc.wantFabric || got.Spec.Registry != tc.wantReg {
				t.Fatalf("got azalea %q fabric %q registry %q, want %q %q %q",
					got.Spec.Azalea.Tag, got.Spec.Fabric.Tag, got.Spec.Registry, tc.wantAzalea, tc.wantFabric, tc.wantReg)
			}
		})
	}
}

func TestLookupRefusesAMissingNamedProfile(t *testing.T) {
	_, err := profile.Lookup(context.Background(), reader(t), bot(&v1alpha1.ProfileRef{Name: "gone"}))
	if err == nil {
		t.Fatal("a bot naming a profile that does not exist should not fall back to the defaults")
	}
}
