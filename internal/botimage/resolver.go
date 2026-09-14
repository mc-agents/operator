package botimage

import (
	"fmt"
	"strings"

	"github.com/mc-agents/operator/api/v1alpha1"
)

const DefaultRegistry = "junhyung.cloud/library"

type Resolver interface {
	Resolve(spec *v1alpha1.MinecraftBotSpec) (string, error)
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
	// Both kinds are built against one protocol version, so both tags carry it.
	var tag string
	switch spec.Kind {
	case v1alpha1.BotKindFabric:
		tag = r.tags.Fabric
	case v1alpha1.BotKindAzalea:
		tag = r.tags.Azalea
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
