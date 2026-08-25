{/*
Expand the name of the chart.
*/}}
{{- define "fabric.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "fabric.fullname" -}}
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
Create chart name and version as used by the chart label.
*/}}
{{- define "fabric.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "fabric.labels" -}}
helm.sh/chart: {{ include "fabric.chart" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}


{{/*
Create the name of the service account to use
*/}}
{{- define "fabric.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "fabric.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}


{{/*
Get IP-addresses of master nodes. If no nodes are returned, we assume this is
a dry-run/template call and return nothing.
*/}}
{{- define "fabric.nodeIPs" -}}
{{- $nodes := lookup "v1" "Node" "" "" -}}
{{- if $nodes -}}
{{- $ips := list -}}
{{- range $node := $nodes.items -}}
  {{- range $label, $value := $.Values.masterNodesLabels }}
  {{- if and (hasKey $node.metadata.labels $label) (or (eq ($value | toString) "") (eq (index $node.metadata.labels $label) ($value | toString))) -}}
    {{- range $address := $node.status.addresses -}}
      {{- if eq $address.type "InternalIP" -}}
        {{- $ips = append $ips $address.address -}}
        {{- break -}}
      {{- end -}}
    {{- end -}}
  {{- end -}}
  {{- end }}
{{- end -}}
{{- if and (eq (len $ips) 0) (not $.Values.masterNodes) -}}
  {{- fail (printf "No nodes found with label '%s'. Please check your masterNodesLabels configuration or ensure master nodes are properly labeled." $.Values.masterNodesLabels) -}}
{{- end -}}
{{ join "," $ips }}
{{- end -}}
{{- end -}}

{{/*
Build hardcodedRequired list for fabric.affinities.nodeAffinity from masterNodesLabels.
Each label gets its own nodeSelectorTerm so multiple labels use OR semantics
(matching the fabric.nodeIPs helper which also uses OR).
Uses Exists operator for empty/nil-value labels and In for specific values.
*/}}
{{- define "fabric.masterNodeRequired" -}}
{{- $terms := list -}}
{{- range $key, $value := .Values.masterNodesLabels -}}
  {{- if eq ($value | toString) "" -}}
    {{- $terms = append $terms (dict "matchExpressions" (list (dict "key" $key "operator" "Exists"))) -}}
  {{- else -}}
    {{- $terms = append $terms (dict "matchExpressions" (list (dict "key" $key "operator" "In" "values" (list ($value | toString))))) -}}
  {{- end -}}
{{- end -}}
{{- $terms | toYaml -}}
{{- end -}}

{{/*
Number of master nodes
*/}}
{{- define "fabric.nodeCount" -}}
  {{- len (split "," ((join "," .Values.masterNodes) | default (include "fabric.nodeIPs" .))) }}
{{- end -}}

{{/*
Get IPs of master nodes from values
*/}}
{{- define "fabric.masterNodes" -}}
  {{- join "," .Values.masterNodes }}
{{- end -}}

{{/*
Environment variables used by the OVN NB/SB database server TLS setup.
*/}}
{{- define "fabric.ovnCentralTLSEnv" -}}
- name: ENABLE_SSL
  value: {{ .Values.networking.enableSsl | quote }}
- name: TLS_MIN_VERSION
  value: {{ .Values.networking.tlsMinVersion | quote }}
- name: TLS_MAX_VERSION
  value: {{ .Values.networking.tlsMaxVersion | quote }}
- name: TLS_CIPHER_SUITES
  value: {{ join "," .Values.networking.tlsCipherSuites | quote }}
{{- end -}}

{{/*
TLS arguments for fabric components that expose HTTPS endpoints.
*/}}
{{- define "fabric.componentTLSArgs" -}}
{{- if .Values.networking.tlsMinVersion }}
- --tls-min-version={{ .Values.networking.tlsMinVersion }}
{{- end }}
{{- if .Values.networking.tlsMaxVersion }}
- --tls-max-version={{ .Values.networking.tlsMaxVersion }}
{{- end }}
{{- if .Values.networking.tlsCipherSuites }}
{{- range .Values.networking.tlsCipherSuites }}
- --tls-cipher-suites={{ . }}
{{- end }}
{{- end }}
{{- end -}}

{{- define "fabric.centralNamespace" -}}
{{- if .Values.central.hcp.enabled -}}
{{- default .Values.namespace .Values.central.hcp.namespace -}}
{{- else -}}
{{- .Values.namespace -}}
{{- end -}}
{{- end -}}

{{- define "fabric.centralReplicas" -}}
{{- if .Values.central.hcp.enabled -}}
{{- .Values.central.hcp.replicas -}}
{{- else -}}
{{- include "fabric.nodeCount" . -}}
{{- end -}}
{{- end -}}

{{- define "fabric.centralRaftAddresses" -}}
{{- $namespace := include "fabric.centralNamespace" . -}}
{{- $addresses := list -}}
{{- range $i := until (int .Values.central.hcp.replicas) -}}
{{- $addresses = append $addresses (printf "ovn-central-%d.ovn-central.%s.svc" $i $namespace) -}}
{{- end -}}
{{- join "," $addresses -}}
{{- end -}}

{{- define "fabric.ovnDbAddresses" -}}
{{- include "fabric.masterNodes" . | default (include "fabric.nodeIPs" .) -}}
{{- end -}}

{{- define "fabric.ovnNbAddress" -}}
{{- if not .Values.central.hcp.nbAddress -}}
{{- fail "central.hcp.nbAddress must be set when central.hcp.enabled is true" -}}
{{- end -}}
{{- .Values.central.hcp.nbAddress -}}
{{- end -}}

{{- define "fabric.ovnSbAddress" -}}
{{- if not .Values.central.hcp.sbAddress -}}
{{- fail "central.hcp.sbAddress must be set when central.hcp.enabled is true" -}}
{{- end -}}
{{- .Values.central.hcp.sbAddress -}}
{{- end -}}

{{- define "fabric.installMode" -}}
{{- .Values.installMode | default "full" -}}
{{- end -}}

{{- define "fabric.renderControlPlane" -}}
{{- $mode := include "fabric.installMode" . -}}
{{- if or (eq $mode "full") (eq $mode "controlPlaneOnly") -}}
true
{{- end -}}
{{- end -}}

{{- define "fabric.renderDataPlane" -}}
{{- $mode := include "fabric.installMode" . -}}
{{- if or (eq $mode "full") (eq $mode "dataPlaneOnly") -}}
true
{{- end -}}
{{- end -}}

{{- define "fabric.renderFullOnly" -}}
{{- if eq (include "fabric.installMode" .) "full" -}}
true
{{- end -}}
{{- end -}}

{{- define "fabric.k8sNodeCount" -}}
{{- $nodes := lookup "v1" "Node" "" "" -}}
{{- if and $nodes $nodes.items -}}
{{- len $nodes.items -}}
{{- else -}}
{{- include "fabric.nodeCount" . -}}
{{- end -}}
{{- end -}}

{{- define "fabric.controllerReplicas" -}}
{{- if eq (include "fabric.installMode" .) "dataPlaneOnly" -}}
{{- min 2 (include "fabric.k8sNodeCount" . | int) -}}
{{- else -}}
{{- include "fabric.nodeCount" . -}}
{{- end -}}
{{- end -}}

{{- define "fabric.ovs-ovn.updateStrategy" -}}
  {{- $ds := lookup "apps/v1" "DaemonSet" $.Values.namespace "ovs-ovn" -}}
  {{- if $ds -}}
    {{- if eq $ds.spec.updateStrategy.type "RollingUpdate" -}}
      RollingUpdate
    {{- else -}}
      {{- $chartVersion := index $ds.metadata.annotations "chart-version" }}
      {{- $newChartVersion := printf "%s-%s" .Chart.Name .Chart.Version }}
      {{- $imageVersion := (index $ds.spec.template.spec.containers 0).image | splitList ":" | last | trimPrefix "v" -}}
      {{- $versionRegex := `^(?P<major>0|[1-9]\d*)\.(?P<minor>0|[1-9]\d*)\.(?P<patch>0|[1-9]\d*)` -}}
      {{- if and (ne $newChartVersion $chartVersion) (regexMatch $versionRegex $imageVersion) -}}
        {{- if regexFind $versionRegex $imageVersion | semverCompare ">= 1.12.0" -}}
          RollingUpdate
        {{- else -}}
          OnDelete
        {{- end -}}
      {{- else -}}
        OnDelete
      {{- end -}}
    {{- end -}}
  {{- else -}}
    {{- $.Values.ovsOvn.updateStrategy.type -}}
  {{- end -}}
{{- end -}}


{{- define "fabric.runAsUser" -}}
  {{- if $.Values.features.enableOvnIpsec -}}
    0
  {{- else -}}
    65534
  {{- end -}}
{{- end -}}

{{- define "fabric.imageSpec" -}}
  {{- $root := .root -}}
  {{- $image := .image | default dict -}}
  {{- $address := get $image "registry" | default $root.Values.global.registry.address -}}
  {{- $repository := .defaultRepository | default $root.Values.global.images.fabric.repository -}}
  {{- $tag := .defaultTag | default $root.Values.global.images.fabric.tag -}}
  {{- $prefix := "" -}}
  {{- if $address -}}
    {{- $prefix = printf "%s/" $address -}}
  {{- end -}}
  {{- dict
      "address" $address
      "prefix" $prefix
      "repository" (get $image "repository" | default $repository)
      "tag" (get $image "tag" | default $tag)
      "pullPolicy" (get $image "pullPolicy" | default $root.Values.image.pullPolicy)
    | toYaml -}}
{{- end -}}

{{/*
Merge hardcoded node affinity expressions with user-provided values.
Usage: include "fabric.affinities.nodeAffinity" (dict "hardcodedPreferred" $hardcodedPreferred "hardcodedRequired" $hardcodedRequired "userPreferred" .Values.component.nodeAffinity.preferredDuringSchedulingIgnoredDuringExecution "userRequired" .Values.component.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution)
*/}}
{{- define "fabric.affinities.nodeAffinity" -}}
{{- $hardcodedPreferred := .hardcodedPreferred | default list -}}
{{- $hardcodedRequired := .hardcodedRequired | default list -}}
{{- $userPreferred := .userPreferred | default list -}}
{{- $userRequired := .userRequired | default list -}}
{{- $mergedPreferred := concat $hardcodedPreferred $userPreferred -}}
{{- $mergedRequired := concat $hardcodedRequired $userRequired -}}
{{- if or $mergedPreferred $mergedRequired -}}
nodeAffinity:
  {{- if $mergedPreferred }}
  preferredDuringSchedulingIgnoredDuringExecution:
    {{- range $mergedPreferred }}
    - preference:
        matchExpressions:
          {{- toYaml .matchExpressions | nindent 10 }}
      weight: {{ .weight | default 100 }}
    {{- end }}
  {{- end }}
  {{- if $mergedRequired }}
  requiredDuringSchedulingIgnoredDuringExecution:
    nodeSelectorTerms:
      {{- range $mergedRequired }}
      - matchExpressions:
          {{- toYaml .matchExpressions | nindent 8 }}
      {{- end }}
  {{- end }}
{{- end -}}
{{- end -}}
