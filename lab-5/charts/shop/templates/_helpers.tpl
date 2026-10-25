{{- define "shop.labels" -}}
app.kubernetes.io/instance: {{ .Release.Name | quote }}
app.kubernetes.io/part-of: shop
app.kubernetes.io/managed-by: {{ .Release.Service | quote }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | quote }}
{{- end -}}
