{{/*
Expand the name of the chart.
*/}}
{{- define "foip-operator.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "foip-operator.fullname" -}}
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

{{/*
Chart label
*/}}
{{- define "foip-operator.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "foip-operator.labels" -}}
helm.sh/chart: {{ include "foip-operator.chart" . }}
{{ include "foip-operator.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "foip-operator.selectorLabels" -}}
app.kubernetes.io/name: {{ include "foip-operator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
foip ServiceAccount name
*/}}
{{- define "foip-operator.controller.serviceAccountName" -}}
{{- if .Values.controller.serviceAccount.create }}
{{- default (printf "%s-operator" (include "foip-operator.fullname" .)) .Values.controller.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.controller.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
node-interface ServiceAccount name
*/}}
{{- define "foip-operator.nodeInterface.serviceAccountName" -}}
{{- if .Values.nodeInterface.serviceAccount.create }}
{{- default (printf "%s-node-interface" (include "foip-operator.fullname" .)) .Values.nodeInterface.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.nodeInterface.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Image reference
*/}}
{{- define "foip-operator.image" -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end }}

{{/*
Metrics bind address
*/}}
{{- define "foip-operator.metricsBindAddress" -}}
{{- if .Values.observability.metrics.enabled -}}
:8443
{{- else -}}
0
{{- end -}}
{{- end }}

{{/*
Observability environment variables
*/}}
{{- define "foip-operator.observabilityEnv" -}}
{{- if and .Values.observability.traces.enabled .Values.observability.otlp.endpoint -}}
{{- $root := .root -}}
{{- $component := .component -}}
{{- $serviceName := default (printf "%s-%s" (include "foip-operator.fullname" $root) $component) $root.Values.observability.serviceName -}}
- name: OTEL_SERVICE_NAME
  value: {{ $serviceName | quote }}
- name: OTEL_RESOURCE_ATTRIBUTES
  value: {{ printf "service.name=%s,service.namespace=%s,service.version=%s,k8s.namespace.name=%s,foip.component=%s" $serviceName $root.Release.Namespace $root.Chart.AppVersion $root.Release.Namespace $component | quote }}
{{- with $root.Values.observability.otlp.endpoint }}
- name: OTEL_EXPORTER_OTLP_ENDPOINT
  value: {{ . | quote }}
- name: OTEL_EXPORTER_OTLP_INSECURE
  value: {{ ternary "true" "false" $root.Values.observability.otlp.insecure | quote }}
{{- end }}
{{- end }}
{{- end }}
