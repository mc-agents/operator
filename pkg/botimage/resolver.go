package botimage

import (
	"fmt"
	"strings"

	"github.com/mc-agents/operator/api/v1alpha1"
)

const DefaultRegistry = "ghcr.io/mc-agents"

type Resolver interface {
	Resolve(spec *v1alpha1.MinecraftBotSpec) (string, error)
}

type Tags struct {
	Mineflayer string
	Fabric     string
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

func (r *DefaultResolver) Resolve(spec *v1alpha1.MinecraftBotSpec) (string, error) {
	repository := spec.Image.Repository
	if repository == "" {
		repository = fmt.Sprintf("%s/bot-%s", r.registry, spec.Kind)
	}
	if ref, ok := pinned(repository); ok {
		return ref, nil
	}

	tag, err := r.tag(spec)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(tag, "sha256:") {
		return repository + "@" + tag, nil
	}
	return repository + ":" + tag, nil
}

func (r *DefaultResolver) tag(spec *v1alpha1.MinecraftBotSpec) (string, error) {
	if spec.Image.Tag != "" {
		return spec.Image.Tag, nil
	}
	switch spec.Kind {
	case v1alpha1.BotKindMineflayer:
		if r.tags.Mineflayer == "" {
			return "", fmt.Errorf("no default tag configured for %s bots", spec.Kind)
		}
		return r.tags.Mineflayer, nil
	case v1alpha1.BotKindFabric:
		if r.tags.Fabric == "" {
			return "", fmt.Errorf("no default tag configured for %s bots", spec.Kind)
		}
		if spec.MinecraftVersion == "" {
			return "", fmt.Errorf("fabric bots need spec.minecraftVersion to pick an image")
		}
		return fmt.Sprintf("%s-mc%s", r.tags.Fabric, spec.MinecraftVersion), nil
	default:
		return "", fmt.Errorf("unknown bot kind %q", spec.Kind)
	}
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
