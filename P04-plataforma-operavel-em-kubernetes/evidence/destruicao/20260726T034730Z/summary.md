# destruicao

terraform destroy retornou código 0.
namespace edge removido: True.
namespace observability removido: True.
nós do cluster kind ainda de pé depois do destroy: 3 (esperado 3, já que destroy é escopo do Terraform, não do kind).

Resultado geral esperado (destruição confirmada e cluster kind sobrevive): True.