// +kubebuilder:object:generate=true
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const GroupName = "mc-agents.dev"

const Version = "v1alpha1"

var (
	SchemeGroupVersion = schema.GroupVersion{Group: GroupName, Version: Version}

	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

	AddToScheme = SchemeBuilder.AddToScheme
)

func Resource(resource string) schema.GroupResource {
	return SchemeGroupVersion.WithResource(resource).GroupResource()
}

func Kind(kind string) schema.GroupKind {
	return SchemeGroupVersion.WithKind(kind).GroupKind()
}

func addKnownTypes(s *runtime.Scheme) error {
	s.AddKnownTypes(SchemeGroupVersion,
		&MinecraftBot{}, &MinecraftBotList{},
		&MinecraftBotPool{}, &MinecraftBotPoolList{},
	)
	metav1.AddToGroupVersion(s, SchemeGroupVersion)
	return nil
}
