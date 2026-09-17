// Package mcpserverspec builds the objects that run one MCPServer in its own namespace. Nothing here
// talks to the API server.
package mcpserverspec

import (
	"cmp"
	"fmt"
	"maps"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/mc-agents/operator/api/v1alpha1"
)

const (
	ContainerName   = "mcp-server"
	ComponentName   = "mcp-server"
	BotLinkPort     = 8765
	DefaultImage    = "junhyung.cloud/mc-agents/mcp-server"
	portMCP         = "mcp"
	portBotLink     = "bot-link"
	volumeTmp       = "tmp"
	defaultMaxBots  = 16
	defaultLogLevel = "INFO"
	defaultFlushMs  = 1000
)

type Defaults struct {
	Repository string
	Tag        string
}

func Name(server *v1alpha1.MCPServer) string {
	return server.Name + "-mcp-server"
}

func GeneratedSecretName(server *v1alpha1.MCPServer) string {
	return Name(server) + "-auth"
}

// LinkSecretName is the Secret bots present in their hello. Separate from the auth token: an agent
// holds the auth token, and a bot that could read it would hold the MCP port too.
func LinkSecretName(server *v1alpha1.MCPServer) string {
	return Name(server) + "-link"
}

func BotServiceName(server *v1alpha1.MCPServer) string {
	return Name(server) + "-bots"
}

func TokenSecret(server *v1alpha1.MCPServer) v1alpha1.SecretKeyRef {
	name := server.Spec.Auth.ExistingSecret
	if name == "" {
		name = GeneratedSecretName(server)
	}
	return v1alpha1.SecretKeyRef{Name: name, Key: cmp.Or(server.Spec.Auth.SecretKey, v1alpha1.DefaultTokenKey)}
}

func Endpoint(server *v1alpha1.MCPServer) string {
	return fmt.Sprintf("http://%s:%d/mcp", host(server), v1alpha1.MCPServerPort)
}

func Provisions(server *v1alpha1.MCPServer) bool {
	return server.Spec.Bots.Provision == nil || *server.Spec.Bots.Provision
}

func Image(server *v1alpha1.MCPServer, defaults Defaults) string {
	repository := cmp.Or(server.Spec.Image.Repository, defaults.Repository, DefaultImage)
	if strings.Contains(repository, "@") || strings.LastIndex(repository, ":") > strings.LastIndex(repository, "/") {
		return repository
	}
	tag := cmp.Or(server.Spec.Image.Tag, defaults.Tag, "latest")
	if strings.HasPrefix(tag, "sha256:") {
		return repository + "@" + tag
	}
	return repository + ":" + tag
}

func SelectorLabels(server *v1alpha1.MCPServer) map[string]string {
	return map[string]string{
		v1alpha1.LabelName:     ComponentName,
		v1alpha1.LabelInstance: server.Name,
	}
}

func Labels(server *v1alpha1.MCPServer) map[string]string {
	labels := SelectorLabels(server)
	labels[v1alpha1.LabelManagedBy] = v1alpha1.ManagedByValue
	labels[v1alpha1.LabelMCPServer] = server.Name
	return labels
}

func ServiceAccount(server *v1alpha1.MCPServer) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		TypeMeta:   typeMeta("v1", "ServiceAccount"),
		ObjectMeta: objectMeta(server, Name(server)),
	}
}

// Service carries the MCP port alone, of whatever type the spec asks for: the bearer token is that
// port's gate. The bot-link port is on BotService.
func Service(server *v1alpha1.MCPServer) *corev1.Service {
	meta := objectMeta(server, Name(server))
	meta.Annotations = maps.Clone(server.Spec.Service.Annotations)
	return &corev1.Service{
		TypeMeta:   typeMeta("v1", "Service"),
		ObjectMeta: meta,
		Spec: corev1.ServiceSpec{
			Type:     cmp.Or(server.Spec.Service.Type, corev1.ServiceTypeClusterIP),
			Selector: SelectorLabels(server),
			Ports: []corev1.ServicePort{
				{Name: portMCP, Port: v1alpha1.MCPServerPort, TargetPort: intstr.FromString(portMCP), Protocol: corev1.ProtocolTCP},
			},
		},
	}
}

// BotService carries the bot-link port and is always ClusterIP. That port has no authentication,
// so a NodePort or LoadBalancer asked for on the MCP port must not take it out of the cluster.
func BotService(server *v1alpha1.MCPServer) *corev1.Service {
	return &corev1.Service{
		TypeMeta:   typeMeta("v1", "Service"),
		ObjectMeta: objectMeta(server, BotServiceName(server)),
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: SelectorLabels(server),
			Ports: []corev1.ServicePort{
				{Name: portBotLink, Port: BotLinkPort, TargetPort: intstr.FromString(portBotLink), Protocol: corev1.ProtocolTCP},
			},
		},
	}
}

// NetworkPolicy is the gate on the bot-link port: only bot pods of the MCPServer's own namespace
// may dial it. A tenant's bots are made in the tenant's namespace and nowhere else, so a bot pod
// from another namespace is another tenant's, whatever its label says. The MCP port stays open to
// everything, since the bearer token is its gate. The kubelet's probes bypass the policy on
// kube-router, Calico and Cilium alike, so the readiness probe needs no rule.
func NetworkPolicy(server *v1alpha1.MCPServer) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		TypeMeta:   typeMeta("networking.k8s.io/v1", "NetworkPolicy"),
		ObjectMeta: objectMeta(server, Name(server)),
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: SelectorLabels(server)},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{
				{
					Ports: []networkingv1.NetworkPolicyPort{{Protocol: ptr(corev1.ProtocolTCP), Port: ptr(intstr.FromString(portBotLink))}},
					From: []networkingv1.NetworkPolicyPeer{{
						// The label the API server keeps on every namespace since 1.21.
						NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{corev1.LabelMetadataName: server.Namespace}},
						PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{v1alpha1.LabelName: v1alpha1.BotPodName}},
					}},
				},
				{
					Ports: []networkingv1.NetworkPolicyPort{{Protocol: ptr(corev1.ProtocolTCP), Port: ptr(intstr.FromString(portMCP))}},
				},
			},
		},
	}
}

// Role lets join-server create the bots it starts and leave-server take them back. Pods are the
// operator's; the server never touches one.
func Role(server *v1alpha1.MCPServer) *rbacv1.Role {
	return &rbacv1.Role{
		TypeMeta:   typeMeta("rbac.authorization.k8s.io/v1", "Role"),
		ObjectMeta: objectMeta(server, Name(server)),
		Rules: []rbacv1.PolicyRule{{
			APIGroups: []string{v1alpha1.GroupName},
			Resources: []string{"minecraftbots"},
			Verbs:     []string{"get", "list", "create", "delete"},
		}},
	}
}

func RoleBinding(server *v1alpha1.MCPServer) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		TypeMeta:   typeMeta("rbac.authorization.k8s.io/v1", "RoleBinding"),
		ObjectMeta: objectMeta(server, Name(server)),
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: Name(server)},
		Subjects: []rbacv1.Subject{{
			Kind:      rbacv1.ServiceAccountKind,
			Name:      Name(server),
			Namespace: server.Namespace,
		}},
	}
}

// GeneratedSecret carries a token made once. The caller creates it only when it is missing, so a
// token an agent was already given keeps working across every later reconcile.
func GeneratedSecret(server *v1alpha1.MCPServer, token string) *corev1.Secret {
	ref := TokenSecret(server)
	return secret(server, GeneratedSecretName(server), ref.Key, token)
}

// LinkSecret carries the token bots present in hello, made once like the auth token: a bot already
// running holds the value it was started with, and rewriting it would unlink every one of them.
func LinkSecret(server *v1alpha1.MCPServer, token string) *corev1.Secret {
	return secret(server, LinkSecretName(server), v1alpha1.DefaultTokenKey, token)
}

func secret(server *v1alpha1.MCPServer, name, key, value string) *corev1.Secret {
	return &corev1.Secret{
		TypeMeta:   typeMeta("v1", "Secret"),
		ObjectMeta: objectMeta(server, name),
		Type:       corev1.SecretTypeOpaque,
		StringData: map[string]string{key: value},
	}
}

func Deployment(server *v1alpha1.MCPServer, defaults Defaults) *appsv1.Deployment {
	// The pod carries the managed-by label so it lands in the operator's filtered cache, where
	// the reconciler reads what the kubelet is stuck on. The selector stays the two stable ones.
	podLabels := maps.Clone(server.Spec.PodLabels)
	if podLabels == nil {
		podLabels = map[string]string{}
	}
	maps.Copy(podLabels, Labels(server))

	return &appsv1.Deployment{
		TypeMeta:   typeMeta("apps/v1", "Deployment"),
		ObjectMeta: objectMeta(server, Name(server)),
		Spec: appsv1.DeploymentSpec{
			// A bot is linked to one pod. A second replica would answer about bots it cannot reach,
			// and rolling a new pod up first would leave the old one holding every bot.
			Replicas: ptr(int32(1)),
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
			Selector: &metav1.LabelSelector{MatchLabels: SelectorLabels(server)},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      podLabels,
					Annotations: maps.Clone(server.Spec.PodAnnotations),
				},
				Spec: corev1.PodSpec{
					ServiceAccountName:           Name(server),
					AutomountServiceAccountToken: ptr(Provisions(server)),
					// A Service named mcp-server in the namespace would otherwise inject MCP_SERVER_PORT
					// and MCP_PORT and override what is set here.
					EnableServiceLinks: ptr(false),
					ImagePullSecrets:   server.Spec.ImagePullSecrets,
					NodeSelector:       server.Spec.NodeSelector,
					Tolerations:        server.Spec.Tolerations,
					Affinity:           server.Spec.Affinity,
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   ptr(true),
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Containers: []corev1.Container{container(server, defaults)},
					Volumes: []corev1.Volume{{
						Name:         volumeTmp,
						VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
					}},
				},
			},
		},
	}
}

func container(server *v1alpha1.MCPServer, defaults Defaults) corev1.Container {
	token := TokenSecret(server)
	return corev1.Container{
		Name:            ContainerName,
		Image:           Image(server, defaults),
		ImagePullPolicy: server.Spec.Image.PullPolicy,
		Ports: []corev1.ContainerPort{
			{Name: portMCP, ContainerPort: v1alpha1.MCPServerPort, Protocol: corev1.ProtocolTCP},
			{Name: portBotLink, ContainerPort: BotLinkPort, Protocol: corev1.ProtocolTCP},
		},
		Env: append(env(server),
			corev1.EnvVar{Name: "MCP_AUTH_TOKEN", ValueFrom: secretKeyRef(token.Name, token.Key)},
			// The server refuses a hello without this token once it is set, and writes the Secret's
			// name into every bot it creates, so the bots it starts are the only ones that link.
			corev1.EnvVar{Name: "BOT_LINK_TOKEN", ValueFrom: secretKeyRef(LinkSecretName(server), v1alpha1.DefaultTokenKey)},
			corev1.EnvVar{Name: "MCP_BOTS_LINK_SECRET", Value: LinkSecretName(server)},
		),
		StartupProbe: &corev1.Probe{
			ProbeHandler:     httpGet("/actuator/health/liveness"),
			PeriodSeconds:    2,
			FailureThreshold: 30,
		},
		LivenessProbe: &corev1.Probe{ProbeHandler: httpGet("/actuator/health/liveness")},
		// Ready means the bot link is accepting. A bot that dials a pod which is not ready gets
		// nothing, and its MinecraftBot would sit at Waiting without saying why.
		ReadinessProbe: &corev1.Probe{
			ProbeHandler:  corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromString(portBotLink)}},
			PeriodSeconds: 5,
		},
		Resources:    resources(server),
		VolumeMounts: []corev1.VolumeMount{{Name: volumeTmp, MountPath: "/tmp"}},
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptr(false),
			ReadOnlyRootFilesystem:   ptr(true),
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		},
	}
}

func env(server *v1alpha1.MCPServer) []corev1.EnvVar {
	provision := "never"
	if Provisions(server) {
		provision = "auto"
	}
	muted := make([]string, 0, len(server.Spec.Feeds.Muted))
	for _, feed := range server.Spec.Feeds.Muted {
		muted = append(muted, string(feed))
	}
	env := []corev1.EnvVar{
		// A token-less server binds loopback on its own; in a pod the Service has to reach it.
		{Name: "MCP_BIND_HOST", Value: "0.0.0.0"},
		{Name: "MCP_PORT", Value: strconv.Itoa(v1alpha1.MCPServerPort)},
		{Name: "BOT_LINK_PORT", Value: strconv.Itoa(BotLinkPort)},
		{Name: "MCP_MAX_BOTS", Value: strconv.Itoa(int(cmp.Or(server.Spec.MaxBots, defaultMaxBots)))},
		{Name: "MCP_BOTS_PROVISION", Value: provision},
		{Name: "MCP_BOTS_NAMESPACE", Value: server.Namespace},
		{Name: "MCP_BOTS_MCP_HOST", Value: botHost(server)},
		{Name: "MCP_LOG_LEVEL", Value: cmp.Or(server.Spec.LogLevel, defaultLogLevel)},
		{Name: "BOT_LINK_REPEAT_FLUSH_MS", Value: strconv.Itoa(int(cmp.Or(server.Spec.Feeds.RepeatFlushMs, defaultFlushMs)))},
		{Name: "BOT_LINK_MUTED_FEEDS", Value: strings.Join(muted, ",")},
	}
	if server.Spec.LogFormat != "" {
		env = append(env, corev1.EnvVar{Name: "LOGGING_STRUCTURED_FORMAT_CONSOLE", Value: server.Spec.LogFormat})
	}
	if ref := server.Spec.Bots.ProfileRef; ref != nil {
		env = append(env,
			corev1.EnvVar{Name: "MCP_BOTS_PROFILE_KIND", Value: string(cmp.Or(ref.Kind, v1alpha1.ProfileKindNamespaced))},
			corev1.EnvVar{Name: "MCP_BOTS_PROFILE_NAME", Value: ref.Name},
		)
	}
	return env
}

func resources(server *v1alpha1.MCPServer) corev1.ResourceRequirements {
	if len(server.Spec.Resources.Requests) > 0 || len(server.Spec.Resources.Limits) > 0 {
		return server.Spec.Resources
	}
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("100m"),
			corev1.ResourceMemory: resource.MustParse("512Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("1Gi"),
		},
	}
}

func host(server *v1alpha1.MCPServer) string {
	return fmt.Sprintf("%s.%s.svc", Name(server), server.Namespace)
}

// botHost is what the server writes into the bots it creates: the ClusterIP Service in front of the
// bot-link port, never the MCP one, which may be a load balancer.
func botHost(server *v1alpha1.MCPServer) string {
	return fmt.Sprintf("%s.%s.svc", BotServiceName(server), server.Namespace)
}

func objectMeta(server *v1alpha1.MCPServer, name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name:      name,
		Namespace: server.Namespace,
		Labels:    Labels(server),
		OwnerReferences: []metav1.OwnerReference{
			*metav1.NewControllerRef(server, v1alpha1.SchemeGroupVersion.WithKind("MCPServer")),
		},
	}
}

func typeMeta(apiVersion, kind string) metav1.TypeMeta {
	return metav1.TypeMeta{APIVersion: apiVersion, Kind: kind}
}

func secretKeyRef(name, key string) *corev1.EnvVarSource {
	return &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: name},
		Key:                  key,
	}}
}

func httpGet(path string) corev1.ProbeHandler {
	return corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: path, Port: intstr.FromString(portMCP)}}
}

func ptr[T any](v T) *T { return &v }
