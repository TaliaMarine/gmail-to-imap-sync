{{- define "gmail-archive-sync.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "gmail-archive-sync.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{- define "gmail-archive-sync.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "gmail-archive-sync.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/* Labels shared by every pod and Service of the release. */}}
{{- define "gmail-archive-sync.selectorLabels" -}}
app.kubernetes.io/name: {{ include "gmail-archive-sync.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "gmail-archive-sync.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "gmail-archive-sync.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{- define "gmail-archive-sync.sessionSecretName" -}}
{{- default (printf "%s-session" (include "gmail-archive-sync.fullname" .)) .Values.sessionKey.secretName }}
{{- end }}

{{- define "gmail-archive-sync.encryptionSecretName" -}}
{{- default (printf "%s-encryption" (include "gmail-archive-sync.fullname" .)) .Values.encryption.secretName }}
{{- end }}

{{/* Image reference. */}}
{{- define "gmail-archive-sync.image" -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end }}

{{/* Environment shared by web and worker processes. */}}
{{- define "gmail-archive-sync.commonEnv" -}}
- name: POD_NAMESPACE
  valueFrom:
    fieldRef:
      fieldPath: metadata.namespace
- { name: HTTP_PORT, value: {{ .Values.http.port | quote }} }
- name: DATABASE_URL
  valueFrom:
    secretKeyRef:
      name: {{ .Values.database.existingSecret.name }}
      key: {{ .Values.database.existingSecret.key }}
{{- if .Values.encryption.existingSecret.name }}
- name: ENCRYPTION_KEYS
  valueFrom:
    secretKeyRef:
      name: {{ .Values.encryption.existingSecret.name }}
      key: {{ .Values.encryption.existingSecret.key }}
{{- else }}
- { name: ENCRYPTION_KEY_SECRET_NAME, value: {{ include "gmail-archive-sync.encryptionSecretName" . | quote }} }
- { name: ENCRYPTION_KEY_SECRET_KEY, value: {{ .Values.encryption.key | quote }} }
{{- end }}
{{- range $key, $value := .Values.env }}
- { name: {{ $key }}, value: {{ $value | quote }} }
{{- end }}
{{- with .Values.extraEnv }}
{{ toYaml . }}
{{- end }}
{{- end }}

{{/* Environment only the web/API process needs: public URL and sign-in. */}}
{{- define "gmail-archive-sync.webEnv" -}}
{{- if .Values.gateway.host }}
- { name: BASE_URL, value: {{ printf "https://%s" .Values.gateway.host | quote }} }
{{- end }}
{{- if .Values.oidc.issuerUrl }}
- { name: OIDC_ISSUER, value: {{ .Values.oidc.issuerUrl | quote }} }
- { name: OIDC_CLIENT_ID, value: {{ .Values.oidc.clientId | quote }} }
{{- if .Values.oidc.clientSecretRef.name }}
- name: OIDC_CLIENT_SECRET
  valueFrom:
    secretKeyRef:
      name: {{ .Values.oidc.clientSecretRef.name }}
      key: {{ .Values.oidc.clientSecretRef.key }}
{{- end }}
{{- with .Values.oidc.allowedEmails }}
- { name: OIDC_ALLOWED_EMAILS, value: {{ join "," . | quote }} }
{{- end }}
{{- with .Values.oidc.allowedGroups }}
- { name: OIDC_ALLOWED_GROUPS, value: {{ join "," . | quote }} }
{{- end }}
- { name: SESSION_KEY_SECRET_NAME, value: {{ include "gmail-archive-sync.sessionSecretName" . | quote }} }
- { name: SESSION_KEY_SECRET_KEY, value: {{ .Values.sessionKey.key | quote }} }
{{- else }}
- { name: AUTH_DISABLED, value: {{ .Values.authDisabled | quote }} }
{{- end }}
{{- end }}

{{/* Reject configurations that cannot work, with a message that says how to fix it. */}}
{{- define "gmail-archive-sync.validate" -}}
{{- if not .Values.database.existingSecret.name }}
{{- fail "database.existingSecret.name is required: a Secret holding the PostgreSQL connection string (key database.existingSecret.key)" }}
{{- end }}
{{- if and (not .Values.oidc.issuerUrl) (not .Values.authDisabled) }}
{{- fail "configure oidc.issuerUrl/oidc.clientId, or set authDisabled=true to run without authentication" }}
{{- end }}
{{- if and .Values.oidc.issuerUrl (not .Values.oidc.clientId) }}
{{- fail "oidc.clientId is required when oidc.issuerUrl is set" }}
{{- end }}
{{- if and .Values.oidc.issuerUrl (not .Values.gateway.host) }}
{{- fail "oidc needs gateway.host: it forms the redirect URL https://<host>/auth/callback" }}
{{- end }}
{{- if and .Values.gateway.enabled (not .Values.gateway.host) }}
{{- fail "gateway.host is required when gateway.enabled=true (or set gateway.enabled=false)" }}
{{- end }}
{{- if and .Values.oidc.clientSecretRef.name (not .Values.oidc.clientSecretRef.key) }}
{{- fail "oidc.clientSecretRef.key is required when oidc.clientSecretRef.name is set" }}
{{- end }}
{{- end }}

{{/* Converts a duration such as 6h, 30m or 90s to seconds for PromQL arithmetic. */}}
{{- define "gmail-archive-sync.seconds" -}}
{{- $d := . | toString }}
{{- if hasSuffix "h" $d }}{{ mul (trimSuffix "h" $d | int) 3600 }}
{{- else if hasSuffix "m" $d }}{{ mul (trimSuffix "m" $d | int) 60 }}
{{- else }}{{ trimSuffix "s" $d | int }}{{ end }}
{{- end }}
