{{- define "mc-agents-operator.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "mc-agents-operator.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "mc-agents-operator.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "mc-agents-operator.labels" -}}
helm.sh/chart: {{ include "mc-agents-operator.chart" . }}
{{ include "mc-agents-operator.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "mc-agents-operator.selectorLabels" -}}
app.kubernetes.io/name: {{ include "mc-agents-operator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "mc-agents-operator.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "mc-agents-operator.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "mc-agents-operator.image" -}}
{{- $tag := default .Chart.AppVersion .Values.image.tag -}}
{{- if not $tag -}}
{{- fail "image.tag is empty and Chart.appVersion is unset, so there is no image to run" -}}
{{- end -}}
{{- printf "%s/%s:%s" .Values.image.registry .Values.image.repository $tag -}}
{{- end -}}

{{- define "mc-agents-operator.watchNamespaces" -}}
{{- .Values.watchNamespaces | concat (list .Values.watchNamespace) | compact | uniq | join "," -}}
{{- end -}}

{{- define "mc-agents-operator.validate" -}}
{{- if and (gt (int .Values.replicaCount) 1) (not .Values.leaderElection.enabled) -}}
{{- fail "replicaCount > 1 without leaderElection.enabled: two reconcilers would race to create and delete the same bot pod" -}}
{{- end -}}
{{- if and .Values.metrics.serviceMonitor.enabled (not .Values.metrics.enabled) -}}
{{- fail "metrics.serviceMonitor.enabled is true but metrics.enabled is false, so there is nothing to scrape" -}}
{{- end -}}
{{- if not .Values.bots.registry -}}
{{- fail "bots.registry is empty, so the operator cannot name a bot image" -}}
{{- end -}}
{{- end -}}
