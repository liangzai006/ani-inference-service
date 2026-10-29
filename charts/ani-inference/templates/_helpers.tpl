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
{{- include "ani-inference.imageReference" (dict "image" .Values.image "field" "image") -}}
{{- end -}}

{{- define "ani-inference.migrationImage" -}}
{{- include "ani-inference.imageReference" (dict "image" .Values.migration.image "field" "migration.image") -}}
{{- end -}}

{{- define "ani-inference.gatewayAuthzImage" -}}
{{- include "ani-inference.imageReference" (dict "image" .Values.gatewayAuthz.image "field" "gatewayAuthz.image") -}}
{{- end -}}

{{- define "ani-inference.imageReference" -}}
{{- $repository := required (printf "%s.repository is required" .field) (trim (default "" .image.repository)) -}}
{{- $digest := trim (default "" .image.digest) -}}
{{- if $digest -}}
{{- if not (regexMatch "^sha256:[a-f0-9]{64}$" $digest) -}}
{{- fail (printf "%s.digest must be sha256 followed by 64 lowercase hexadecimal characters" .field) -}}
{{- end -}}
{{- printf "%s@%s" $repository $digest -}}
{{- else -}}
{{- printf "%s:%s" $repository (required (printf "%s.tag is required when digest is empty" .field) (trim (default "" .image.tag))) -}}
{{- end -}}
{{- end -}}

{{- define "ani-inference.gatewayAuthzServiceName" -}}
{{- printf "%s-authz" (include "ani-inference.fullname" .) -}}
{{- end -}}

{{- define "ani-inference.higressRoleName" -}}
{{- printf "%s-%s-higress-rate-limit" (include "ani-inference.fullname" .) .Release.Namespace | trunc 63 | trimSuffix "-" -}}
{{- end -}}
