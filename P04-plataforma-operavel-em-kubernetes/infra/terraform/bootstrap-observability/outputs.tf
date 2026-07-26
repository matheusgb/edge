output "observability_namespace" {
  description = "Namespace onde Prometheus, Grafana, ingress-nginx e metrics-server foram instalados."
  value       = kubernetes_namespace.observability.metadata[0].name
}

output "grafana_service_name" {
  description = "Nome do Service do Grafana (para port-forward manual)."
  value       = "kube-prometheus-stack-grafana"
}

output "prometheus_service_name" {
  description = "Nome do Service do Prometheus (para port-forward manual ou scraping externo)."
  value       = "kube-prometheus-stack-prometheus"
}
