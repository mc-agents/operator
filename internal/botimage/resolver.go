package botimage

import (
	"cmp"
	"fmt"
	"strings"

	"github.com/mc-agents/operator/api/v1alpha1"
)

const DefaultRegistry = "junhyung.cloud/mc-agents"

type Resolver interface {
	Resolve(spec *v1alpha1.MinecraftBotSpec, profile *v1alpha1.BotProfileSpec) (string, error)
}

type Tags struct {
	Fabric string
	Azalea string
}

type DefaultResolver struct {
	registry string
	tags     Tags
}

func NewResolver(registry string, tags Tags) *DefaultResolver {
	if registry == "" {
		registry = DefaultRegistry
	}
	return &DefaultResolver{registry: strings.TrimSuffix(registry, "/"), tags: tags}
}

func (r *DefaultResolver) Resolve(spec *v1alpha1.MinecraftBotSpec, profile *v1alpha1.BotProfileSpec) (string, error) {
	if profile == nil {
		profile = &v1alpha1.BotProfileSpec{}
	}
	repository := spec.Image.Repository
	if repository == "" {
		registry := r.registry
		if profile.Registry != "" {
			registry = strings.TrimSuffix(profile.Registry, "/")
		}
		repository = fmt.Sprintf("%s/bot-%s", registry, spec.Kind)
	}
	if ref, ok := pinned(repository); ok {
		return ref, nil
	}

	tag, err := r.tag(spec, profile)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(tag, "sha256:") {
		return repository + "@" + tag, nil
	}
	return repository + ":" + tag, nil
}

func (r *DefaultResolver) tag(spec *v1alpha1.MinecraftBotSpec, profile *v1alpha1.BotProfileSpec) (string, error) {
	if spec.Image.Tag != "" {
		return spec.Image.Tag, nil
	}
	// Both kinds are built against one protocol version, so both tags carry it.
	var tag string
	switch spec.Kind {
	case v1alpha1.BotKindFabric:
		tag = cmp.Or(profile.Fabric.Tag, r.tags.Fabric)
	case v1alpha1.BotKindAzalea:
		tag = cmp.Or(profile.Azalea.Tag, r.tags.Azalea)
	default:
		return "", fmt.Errorf("unknown bot kind %q", spec.Kind)
	}
	if tag == "" {
		return "", fmt.Errorf("no default tag configured for %s bots", spec.Kind)
	}
	if spec.MinecraftVersion == "" {
		return "", fmt.Errorf("%s bots need spec.minecraftVersion to pick an image", spec.Kind)
	}
	return fmt.Sprintf("%s-mc%s", tag, spec.MinecraftVersion), nil
}

func pinned(repository string) (string, bool) {
	if strings.Contains(repository, "@sha256:") {
		return repository, true
	}
	if i := strings.LastIndex(repository, ":"); i > strings.LastIndex(repository, "/") {
		return repository, true
	}
	return "", false
}
