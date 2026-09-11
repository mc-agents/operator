package v1alpha1

const (
	LabelBot       = GroupName + "/bot"
	LabelPool      = GroupName + "/pool"
	LabelBotKind   = GroupName + "/kind"
	LabelManagedBy = "app.kubernetes.io/managed-by"
	LabelName      = "app.kubernetes.io/name"

	AnnotationSpecHash = GroupName + "/spec-hash"

	ManagedByValue = "mc-agents-operator"
	BotPodName     = "minecraft-bot"
)
