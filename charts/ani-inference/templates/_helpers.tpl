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

{{- define "ani-inference.serviceAccountName" -}}
{{- default (include "ani-inference.fullname" .) .Values.serviceAccount.name -}}
{{- end -}}

{{- define "ani-inference.runtimeServiceAccountName" -}}
{{- default (printf "%s-runtime" (include "ani-inference.fullname" .)) .Values.runtimeServiceAccount.name -}}
{{- end -}}

{{- define "ani-inference.image" -}}
{{- printf "%s:%s" .Values.image.repository .Values.image.tag -}}
{{- end -}}

{{- define "ani-inference.migrationImage" -}}
{{- printf "%s:%s" .Values.migration.image.repository .Values.migration.image.tag -}}
{{- end -}}

{{- define "ani-inference.gatewayAuthzImage" -}}
{{- printf "%s:%s" .Values.gatewayAuthz.image.repository .Values.gatewayAuthz.image.tag -}}
{{- end -}}

{{- define "ani-inference.gatewayAuthzServiceName" -}}
{{- printf "%s-authz" (include "ani-inference.fullname" .) -}}
{{- end -}}

{{- define "ani-inference.higressRoleName" -}}
{{- printf "%s-%s-higress-rate-limit" (include "ani-inference.fullname" .) .Release.Namespace | trunc 63 | trimSuffix "-" -}}
{{- end -}}
