output "edge_namespace" {
  value = kubernetes_namespace.edge.metadata[0].name
}

output "origin_service" {
  value = "${kubernetes_service_v1.origin.metadata[0].name}.${kubernetes_namespace.edge.metadata[0].name}.svc.cluster.local"
}

output "edge_service" {
  value = "${kubernetes_service_v1.edge.metadata[0].name}.${kubernetes_namespace.edge.metadata[0].name}.svc.cluster.local"
}

output "origin_hpa_name" {
  value = kubernetes_horizontal_pod_autoscaler_v2.origin.metadata[0].name
}
