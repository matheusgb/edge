# Testes de plano: contagem/atributos de recursos, não correção contra
# o cluster real (isso é escopo dos scripts de experimento).
provider "kubernetes" {
  config_path    = "~/.kube/config"
  config_context = "kind-edge-lab"
}

run "plano_cria_workloads_com_seguranca_padrao" {
  command = plan

  assert {
    condition     = kubernetes_namespace.edge.metadata[0].name == "edge"
    error_message = "namespace da plataforma deveria se chamar 'edge' por padrão"
  }

  assert {
    condition     = kubernetes_deployment_v1.origin.spec[0].replicas == "2"
    error_message = "origin deveria começar com origin_replicas_min (2) réplicas"
  }

  assert {
    condition     = kubernetes_deployment_v1.origin.spec[0].template[0].spec[0].container[0].image_pull_policy == "Never"
    error_message = "imagens carregadas via kind load docker-image não devem tentar pull de registry"
  }

  assert {
    condition     = kubernetes_deployment_v1.origin.spec[0].template[0].spec[0].container[0].security_context[0].read_only_root_filesystem == true
    error_message = "container da origem deveria ter filesystem somente leitura"
  }

  assert {
    condition     = kubernetes_deployment_v1.origin.spec[0].template[0].spec[0].security_context[0].run_as_non_root == true
    error_message = "pod da origem deveria rodar como não-root"
  }

  assert {
    condition     = length(kubernetes_deployment_v1.origin.spec[0].template[0].spec[0].container[0].security_context[0].capabilities[0].drop) == 1
    error_message = "container da origem deveria remover todas as capabilities (ALL)"
  }
}

run "hpa_respeita_variaveis_min_max" {
  command = plan

  variables {
    origin_replicas_min = 3
    origin_replicas_max = 12
  }

  assert {
    condition     = kubernetes_horizontal_pod_autoscaler_v2.origin.spec[0].min_replicas == 3
    error_message = "HPA da origem deveria respeitar origin_replicas_min"
  }

  assert {
    condition     = kubernetes_horizontal_pod_autoscaler_v2.origin.spec[0].max_replicas == 12
    error_message = "HPA da origem deveria respeitar origin_replicas_max"
  }
}

run "pdb_garante_minimo_disponivel" {
  command = plan

  assert {
    condition     = kubernetes_pod_disruption_budget_v1.origin.spec[0].min_available == "1"
    error_message = "PDB da origem deveria garantir ao menos 1 réplica disponível durante disrupções"
  }

  assert {
    condition     = kubernetes_pod_disruption_budget_v1.edge.spec[0].min_available == "1"
    error_message = "PDB da borda deveria garantir ao menos 1 réplica disponível durante disrupções"
  }
}

run "networkpolicy_default_deny_cobre_ingress_e_egress" {
  command = plan

  assert {
    condition     = length(kubernetes_network_policy_v1.default_deny.spec[0].policy_types) == 2
    error_message = "a policy default-deny deveria cobrir Ingress e Egress"
  }

  assert {
    condition     = kubernetes_network_policy_v1.allow_edge_to_origin.spec[0].pod_selector[0].match_labels["app"] == "origin"
    error_message = "allow-edge-to-origin deveria selecionar pods app=origin"
  }
}

run "rbac_experiments_e_somente_leitura" {
  command = plan

  assert {
    condition     = contains(kubernetes_role.experiments_read_only.rule[0].verbs, "get")
    error_message = "role de experimentos deveria permitir leitura (get)"
  }

  assert {
    condition     = !contains(kubernetes_role.experiments_read_only.rule[0].verbs, "delete")
    error_message = "role de experimentos não deveria permitir delete: precisa ser restrita a leitura"
  }
}
