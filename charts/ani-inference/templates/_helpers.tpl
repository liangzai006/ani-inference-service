{{/* The release name is the resource prefix so installs are predictable. */}}
{{- define "ani-inference.name" -}}
{{- default "ani-inference" .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "ani-inference.fullname" -}}
{{- default .Release.Name .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "ani-inference.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "ani-inference.labels" -}}
helm.sh/chart: {{ include "ani-inference.chart" . }}
app.kubernetes.io/name: {{ include "ani-inference.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "ani-inference.selectorLabels" -}}
app.kubernetes.io/name: {{ include "ani-inference.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "ani-inference.image" -}}
{{- $image := .Values.image.repository -}}
{{- if .Values.image.tag }}{{- $image = printf "%s:%s" $image .Values.image.tag }}{{- end }}
{{- if .Values.image.digest }}{{- $image = printf "%s@%s" $image .Values.image.digest }}{{- end }}
{{- $image -}}
{{- end -}}

{{- define "ani-inference.higressRoleName" -}}
{{- printf "%s-%s-higress-rate-limit" (include "ani-inference.fullname" .) .Release.Namespace | trunc 63 | trimSuffix "-" -}}
{{- end -}}
