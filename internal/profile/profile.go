// Package profile finds the profiles a MinecraftBot takes its defaults from and folds them into
// its spec.
package profile

import (
	"context"
	"fmt"
	"maps"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mc-agents/operator/api/v1alpha1"
)

type Resolved struct {
	Spec v1alpha1.BotProfileSpec
	// Nearest first, as Kind/name.
	Sources []string
}

// Lookup reads the profile the bot names, or the namespace default when it names none, and then the
// cluster default beneath it. A named profile that does not exist is an error; a missing default is
// not.
func Lookup(ctx context.Context, reader client.Reader, bot *v1alpha1.MinecraftBot) (Resolved, error) {
	var layers []layer
	ref := bot.Spec.ProfileRef
	if ref != nil {
		l, found, err := read(ctx, reader, kindOf(ref), bot.Namespace, ref.Name)
		if err != nil {
			return Resolved{}, err
		}
		if !found {
			return Resolved{}, fmt.Errorf("%s %q not found", kindOf(ref), ref.Name)
		}
		layers = append(layers, l)
	} else {
		l, found, err := read(ctx, reader, v1alpha1.ProfileKindNamespaced, bot.Namespace, v1alpha1.DefaultProfileName)
		if err != nil {
			return Resolved{}, err
		}
		if found {
			layers = append(layers, l)
		}
	}

	if ref == nil || kindOf(ref) != v1alpha1.ProfileKindCluster || ref.Name != v1alpha1.DefaultProfileName {
		l, found, err := read(ctx, reader, v1alpha1.ProfileKindCluster, "", v1alpha1.DefaultProfileName)
		if err != nil {
			return Resolved{}, err
		}
		if found {
			layers = append(layers, l)
		}
	}

	var out Resolved
	for _, l := range layers {
		out.Spec = Merge(out.Spec, l.spec)
		out.Sources = append(out.Sources, l.source)
	}
	return out, nil
}

type layer struct {
	spec   v1alpha1.BotProfileSpec
	source string
}

func read(ctx context.Context, reader client.Reader, kind v1alpha1.ProfileKind, namespace, name string) (layer, bool, error) {
	var spec v1alpha1.BotProfileSpec
	var err error
	switch kind {
	case v1alpha1.ProfileKindCluster:
		var p v1alpha1.ClusterMinecraftBotProfile
		err = reader.Get(ctx, client.ObjectKey{Name: name}, &p)
		spec = p.Spec
	default:
		var p v1alpha1.MinecraftBotProfile
		err = reader.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &p)
		spec = p.Spec
	}
	if apierrors.IsNotFound(err) {
		return layer{}, false, nil
	}
	if err != nil {
		return layer{}, false, fmt.Errorf("get %s %q: %w", kind, name, err)
	}
	return layer{spec: spec, source: string(kind) + "/" + name}, true, nil
}

func kindOf(ref *v1alpha1.ProfileRef) v1alpha1.ProfileKind {
	if ref.Kind == "" {
		return v1alpha1.ProfileKindNamespaced
	}
	return ref.Kind
}

// Merge keeps every field near already sets and takes the rest from far. Labels and annotations
// merge key by key; every other field is taken whole or not at all.
func Merge(near, far v1alpha1.BotProfileSpec) v1alpha1.BotProfileSpec {
	out := *near.DeepCopy()
	far = *far.DeepCopy()
	out.Registry = first(out.Registry, far.Registry)
	out.Fabric.Tag = first(out.Fabric.Tag, far.Fabric.Tag)
	out.Azalea.Tag = first(out.Azalea.Tag, far.Azalea.Tag)
	if isEmpty(out.Fabric.Resources.Requests, out.Fabric.Resources.Limits) {
		out.Fabric.Resources = far.Fabric.Resources
	}
	if isEmpty(out.Azalea.Resources.Requests, out.Azalea.Resources.Limits) {
		out.Azalea.Resources = far.Azalea.Resources
	}
	if out.Fabric.Render == nil {
		out.Fabric.Render = far.Fabric.Render
	}
	if out.Fabric.Assets == nil {
		out.Fabric.Assets = far.Fabric.Assets
	}
	if out.ImagePullSecrets == nil {
		out.ImagePullSecrets = far.ImagePullSecrets
	}
	if out.NodeSelector == nil {
		out.NodeSelector = far.NodeSelector
	}
	if out.Tolerations == nil {
		out.Tolerations = far.Tolerations
	}
	if out.Affinity == nil {
		out.Affinity = far.Affinity
	}
	out.PodLabels = under(out.PodLabels, far.PodLabels)
	out.PodAnnotations = under(out.PodAnnotations, far.PodAnnotations)
	return out
}

// Apply returns a copy of bot whose spec takes from p what it leaves empty. The image tag is not
// among them: a profile's tag is a default the Minecraft version is still appended to, and
// spec.image.tag is used as written.
func Apply(bot *v1alpha1.MinecraftBot, p v1alpha1.BotProfileSpec) *v1alpha1.MinecraftBot {
	out := bot.DeepCopy()
	p = *p.DeepCopy()
	spec := &out.Spec
	if isEmpty(spec.Resources.Requests, spec.Resources.Limits) {
		switch spec.Kind {
		case v1alpha1.BotKindFabric:
			spec.Resources = p.Fabric.Resources
		case v1alpha1.BotKindAzalea:
			spec.Resources = p.Azalea.Resources
		}
	}
	if spec.Kind == v1alpha1.BotKindFabric {
		if spec.Render == nil {
			spec.Render = p.Fabric.Render
		}
		if spec.Assets == nil {
			spec.Assets = p.Fabric.Assets
		}
	}
	if spec.ImagePullSecrets == nil {
		spec.ImagePullSecrets = p.ImagePullSecrets
	}
	if spec.NodeSelector == nil {
		spec.NodeSelector = p.NodeSelector
	}
	if spec.Tolerations == nil {
		spec.Tolerations = p.Tolerations
	}
	if spec.Affinity == nil {
		spec.Affinity = p.Affinity
	}
	spec.PodLabels = under(spec.PodLabels, p.PodLabels)
	spec.PodAnnotations = under(spec.PodAnnotations, p.PodAnnotations)
	return out
}

func first(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func isEmpty[M ~map[K]V, K comparable, V any](requests, limits M) bool {
	return len(requests) == 0 && len(limits) == 0
}

func under(near, far map[string]string) map[string]string {
	if len(far) == 0 {
		return near
	}
	out := maps.Clone(far)
	maps.Copy(out, near)
	return out
}
