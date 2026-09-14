{{/* 完整名称 */}}
{{- define "gpu-descheduler-webhook.fullname" -}}
{{- default .Chart.Name .Values.fullnameOverride -}}
{{- end -}}

{{/* 基础标签 */}}
{{- define "gpu-descheduler-webhook.labels" -}}
app: {{ include "gpu-descheduler-webhook.fullname" . }}
chart: {{ .Chart.Name }}-{{ .Chart.Version }}
release: {{ .Release.Name }}
{{- end -}}

{{/* Pod选择器标签 */}}
{{- define "gpu-descheduler-webhook.selectorLabels" -}}
app: {{ include "gpu-descheduler-webhook.fullname" . }}
release: {{ .Release.Name }}
{{- end -}}
