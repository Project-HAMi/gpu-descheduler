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

{{/* certgen image of the TLS hook Jobs, pinned by digest unless patch.image.tag is set */}}
{{- define "gpu-descheduler-webhook.certgenImage" -}}
{{- $image := .Values.patch.image | default dict -}}
{{- $tag := $image.tag | default "v1.1.1@sha256:64d8c73dca984af206adf9d6d7e46aa550362b1d7a01f3a0a91b20cc67868660" -}}
{{ $image.registry | default "docker.io" }}/{{ $image.repository | default "liangjw/kube-webhook-certgen" }}:{{ $tag }}
{{- end -}}

{{/* Service account of the TLS hook Jobs */}}
{{- define "gpu-descheduler-webhook.admissionServiceAccount" -}}
{{ include "gpu-descheduler-webhook.fullname" . }}-admission
{{- end -}}
