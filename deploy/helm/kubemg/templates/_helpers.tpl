{{/*
Names. A release called "kubemg" is not renamed "kubemg-kubemg".
*/}}
{{- define "kubemg.name" -}}
{{- default "kubemg" .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "kubemg.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := include "kubemg.name" . -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "kubemg.postgresql.fullname" -}}
{{- printf "%s-postgresql" (include "kubemg.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "kubemg.serviceAccountName" -}}
{{- default (include "kubemg.fullname" .) .Values.serviceAccount.name -}}
{{- end -}}

{{- define "kubemg.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "kubemg.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "kubemg.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kubemg.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
The server's pods, and only them. Without the component the Service would also
select the in-chart PostgreSQL pod, which carries the same name and instance.
Selectors are immutable, so this is fixed from the first release.
*/}}
{{- define "kubemg.serverSelectorLabels" -}}
{{ include "kubemg.selectorLabels" . }}
app.kubernetes.io/component: server
{{- end -}}

{{/*
The version every KubeMG image is released at together: the server, the agent
and the browser shell share one tag.
*/}}
{{- define "kubemg.tag" -}}
{{- default .Chart.AppVersion .Values.image.tag -}}
{{- end -}}

{{- define "kubemg.image" -}}
{{- $registry := default .Values.image.registry .Values.global.imageRegistry -}}
{{- if .Values.image.digest -}}
{{- printf "%s/%s@%s" $registry .Values.image.repository .Values.image.digest -}}
{{- else -}}
{{- printf "%s/%s:%s" $registry .Values.image.repository (include "kubemg.tag" .) -}}
{{- end -}}
{{- end -}}

{{- define "kubemg.postgresql.image" -}}
{{- $image := .Values.postgresql.image -}}
{{- printf "%s/%s:%s" (default $image.registry .Values.global.imageRegistry) $image.repository $image.tag -}}
{{- end -}}

{{/*
The images the console hands out. Each is empty — the server's own built-in
default — unless set explicitly or a mirror is configured. Under a mirror the
repository paths are the published ones; the debug image's name and tag mirror
defaultDebugImage in backend/pkg/config/config.go and have to move with it.
*/}}
{{- define "kubemg.agentImage" -}}
{{- if .Values.agent.image -}}
{{- .Values.agent.image -}}
{{- else if .Values.global.imageRegistry -}}
{{- printf "%s/kubemg/kubemg-agent:%s" .Values.global.imageRegistry (include "kubemg.tag" .) -}}
{{- end -}}
{{- end -}}

{{- define "kubemg.shellImage" -}}
{{- if .Values.shell.image -}}
{{- .Values.shell.image -}}
{{- else if .Values.global.imageRegistry -}}
{{- printf "%s/kubemg/kubemg-shell:%s" .Values.global.imageRegistry (include "kubemg.tag" .) -}}
{{- end -}}
{{- end -}}

{{- define "kubemg.debugImage" -}}
{{- if .Values.debugImage -}}
{{- .Values.debugImage -}}
{{- else if .Values.global.imageRegistry -}}
{{- printf "%s/library/busybox:1.36" .Values.global.imageRegistry -}}
{{- end -}}
{{- end -}}

{{- define "kubemg.passthrough" -}}
{{- if eq .Values.tls.mode "passthrough" -}}true{{- end -}}
{{- end -}}

{{- define "kubemg.portName" -}}
{{- if include "kubemg.passthrough" . -}}https{{- else -}}http{{- end -}}
{{- end -}}

{{- define "kubemg.servicePort" -}}
{{- if .Values.service.port -}}
{{- .Values.service.port -}}
{{- else if include "kubemg.passthrough" . -}}
443
{{- else -}}
80
{{- end -}}
{{- end -}}

{{- define "kubemg.publicHost" -}}
{{- (urlParse .Values.publicURL).hostname -}}
{{- end -}}

{{/*
The chart's own Secret carries only what was given as a value. It is rendered
when it has anything to carry, and every reference below points at the key it
was given under.
*/}}
{{- define "kubemg.secretName" -}}
{{- include "kubemg.fullname" . -}}
{{- end -}}

{{- define "kubemg.secretData" -}}
{{- $data := dict -}}
{{- if and .Values.database.password (not .Values.database.existingSecret) -}}
{{- $_ := set $data "DB_PASSWORD" .Values.database.password -}}
{{- end -}}
{{- if not .Values.secrets.existingSecret -}}
{{- with .Values.secrets.jwtSecret }}{{ $_ := set $data "JWT_SECRET" . }}{{ end -}}
{{- with .Values.secrets.secretKey }}{{ $_ := set $data "KUBEMG_SECRET_KEY" . }}{{ end -}}
{{- with .Values.secrets.sessionRecordingKey }}{{ $_ := set $data "KUBEMG_SESSION_RECORDING_KEY" . }}{{ end -}}
{{- with .Values.secrets.adminPassword }}{{ $_ := set $data "KUBEMG_ADMIN_PASSWORD" . }}{{ end -}}
{{- end -}}
{{- toYaml $data -}}
{{- end -}}

{{- define "kubemg.databasePasswordRef" -}}
{{- if .Values.database.existingSecret -}}
name: {{ .Values.database.existingSecret }}
key: {{ .Values.database.existingSecretPasswordKey }}
{{- else -}}
name: {{ include "kubemg.secretName" . }}
key: DB_PASSWORD
{{- end -}}
{{- end -}}

{{- define "kubemg.databaseHost" -}}
{{- if .Values.postgresql.enabled -}}
{{- include "kubemg.postgresql.fullname" . -}}
{{- else -}}
{{- .Values.database.host -}}
{{- end -}}
{{- end -}}

{{/*
The in-chart PostgreSQL does not serve TLS; it is reached over the pod network
only.
*/}}
{{- define "kubemg.databaseSSLMode" -}}
{{- if .Values.postgresql.enabled -}}disable{{- else -}}{{ .Values.database.sslMode }}{{- end -}}
{{- end -}}

{{/*
Refusals. Each is a configuration that would install cleanly and then fail
somewhere less obvious — at an agent's handshake, a kubeconfig, or a database
connection — so it fails here, with the fix named.
*/}}
{{- define "kubemg.validate" -}}
{{- if not .Values.publicURL -}}
{{- fail "publicURL is required: it is the address every agent dials, and the certificate KubeMG mints covers its host. Set it to the https:// address this install will be reached on." -}}
{{- end -}}
{{- $url := urlParse .Values.publicURL -}}
{{- if or (ne $url.scheme "https") (not $url.hostname) -}}
{{- fail (printf "publicURL must be an https:// URL with a host (got %q): client-go refuses to send a bearer token over plain http, so generated kubeconfigs and kubectl exec would not work." .Values.publicURL) -}}
{{- end -}}
{{- if and .Values.postgresql.enabled .Values.database.host -}}
{{- fail "database.host and postgresql.enabled are both set; choose one. The in-chart PostgreSQL is for evaluation, an external one is for everything else." -}}
{{- end -}}
{{- if not (or .Values.postgresql.enabled .Values.database.host) -}}
{{- fail "no database: set database.host to an external PostgreSQL 16, or postgresql.enabled=true to run one in the chart for evaluation." -}}
{{- end -}}
{{- if not (or .Values.database.password .Values.database.existingSecret) -}}
{{- fail "no database password: set database.existingSecret (preferred) or database.password. The chart does not generate one, because a password generated at render time changes on every render and PostgreSQL only reads it once." -}}
{{- end -}}
{{- if and (not (include "kubemg.passthrough" .)) .Values.tls.existingSecret -}}
{{- fail "tls.existingSecret is set in edge mode, where this pod serves plain HTTP and never presents it. Put the certificate on the ingress (ingress.tlsSecretName), or use tls.mode=passthrough." -}}
{{- end -}}
{{- if and .Values.ingress.enabled (include "kubemg.passthrough" .) .Values.ingress.tlsSecretName -}}
{{- fail "ingress.tlsSecretName is set in passthrough mode, where the ingress does not terminate TLS. Use tls.existingSecret for the certificate KubeMG serves, or tls.mode=edge." -}}
{{- end -}}
{{- end -}}
