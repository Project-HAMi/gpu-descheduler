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

{{/* Namespace for all namespaced resources: values.namespace, or the release namespace */}}
{{- define "gpu-descheduler-webhook.namespace" -}}
{{- default .Release.Namespace .Values.namespace -}}
{{- end -}}

{{/* Webhook container image */}}
{{- define "gpu-descheduler-webhook.image" -}}
{{- $repo := .Values.image.repository | default "projecthami/gpu-descheduler-webhook" -}}
{{- $tag := .Values.image.tag | default (printf "v%s" (trimPrefix "v" .Chart.AppVersion)) -}}
{{- if .Values.image.registry -}}
{{ .Values.image.registry }}/{{ $repo }}:{{ $tag }}
{{- else -}}
{{ $repo }}:{{ $tag }}
{{- end -}}
{{- end -}}

{{/* Secret that holds the webhook certificate */}}
{{- define "gpu-descheduler-webhook.certSecret" -}}
{{- if and .Values.certManager .Values.certManager.enabled -}}
{{ .Values.certManager.secretName | default "gpu-descheduler-webhook-tls" }}
{{- else -}}
{{ include "gpu-descheduler-webhook.fullname" . }}-cert
{{- end -}}
{{- end -}}

{{/* true when the descheduler issues its own certificate */}}
{{- define "gpu-descheduler-webhook.manageCerts" -}}
{{- if not (and .Values.certManager .Values.certManager.enabled) }}true{{ end -}}
{{- end -}}
