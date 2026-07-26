output "observability_namespace" {
  value = module.bootstrap_observability.observability_namespace
}

output "edge_namespace" {
  value = module.edge_platform.edge_namespace
}

output "origin_service" {
  value = module.edge_platform.origin_service
}

output "edge_service" {
  value = module.edge_platform.edge_service
}
