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

{{/* The same runtime rules are bound only within an actual tenant. */}}
{{- define "ani-inference.runtimeRules" -}}
- apiGroups: [""]
  resources: ["serviceaccounts", "configmaps"]
  verbs: ["get"]
- apiGroups: ["gateway.networking.k8s.io"]
  resources: ["httproutes"]
  verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
# Central Higress routes need the original publisher's target-tenant grant.
- apiGroups: ["gateway.networking.k8s.io"]
  resources: ["referencegrants"]
  verbs: ["get", "create", "delete"]
- apiGroups: ["ani.kubercloud.com"]
  resources: ["inferenceservices"]
  verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
- apiGroups: ["ani.kubercloud.com"]
  resources: ["inferenceservices/status", "inferenceservices/finalizers"]
  verbs: ["get", "update", "patch"]
- apiGroups: ["serving.kserve.io"]
  resources: ["inferenceservices", "llminferenceservices"]
  verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
- apiGroups: ["serving.kserve.io"]
  resources: ["inferenceservices/status", "inferenceservices/finalizers", "llminferenceservices/status", "llminferenceservices/finalizers"]
  verbs: ["get", "update", "patch"]
- apiGroups: ["apps"]
  resources: ["deployments"]
  verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
- apiGroups: ["batch"]
  resources: ["jobs"]
  verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
- apiGroups: ["leaderworkerset.x-k8s.io"]
  resources: ["leaderworkersets"]
  verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
- apiGroups: [""]
  resources: ["pods"]
  verbs: ["get", "list", "watch", "delete"]
# Secret write access remains only for the model materializer's own
# per-generation download credential and its cleanup. Inference never writes
# the shared tenant image pull Secret: Resource is its single writer, and
# Inference only reads the ensured object by the frozen reference.
- apiGroups: [""]
  resources: ["services", "persistentvolumeclaims", "secrets"]
  verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
- apiGroups: ["apps"]
  resources: ["replicasets", "statefulsets"]
  verbs: ["get"]
- apiGroups: [""]
  resources: ["pods/log"]
  verbs: ["get"]
- apiGroups: [""]
  resources: ["events"]
  verbs: ["get", "list"]
{{- end -}}
