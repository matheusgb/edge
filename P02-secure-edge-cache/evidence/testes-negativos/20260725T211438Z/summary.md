# Resumo: testes negativos da borda

13 vetores, 0 falha(s).

| Vetor | Categoria | O que tenta | Esperado | Obtido | Resultado |
|---|---|---|---|---|---|
| sem-token | token | requisição a objeto protegido sem nenhum token | 403 | 403 | passou |
| assinatura-alterada | token | um caractere trocado na assinatura HMAC | 403 | 403 | passou |
| chave-desconhecida | token | token apontando para um identificador de chave inexistente | 403 | 403 | passou |
| token-de-outro-objeto | token | token válido de um objeto, colado na URL de outro | 403 | 403 | passou |
| token-expirado | token | token de 1s usado 2s depois de emitido | 403 | 403 | passou |
| metodo-nao-permitido | borda | POST em rota de leitura, mesmo com token válido de GET | 403 ou 405 | 403 | passou |
| corpo-grande | borda | requisição com 1 MiB de corpo contra o limite de client_max_body_size | 413 ou conexão recusada | 413 | passou |
| host-desconhecido | poisoning | Host inventado, tentando criar uma entrada de cache paralela | conexão encerrada ou 4xx | erro de transporte: Get "http://127.0.0.1:8080/objects/neg-1KiB-1785014076-host.bin?(token omitido)": EOF | passou |
| query-nao-entra-na-chave | poisoning | parâmetro extra na query não deve criar uma segunda entrada de cache | 200 com X-Cache-Status: HIT | 200 (cache: HIT) | passou |
| caminho-codificado | poisoning | travessia codificada que resolve para o mesmo objeto | 403, ou 200 na MESMA entrada de cache (HIT) | 200 (cache: HIT) | passou |
| header-forjado | poisoning | headers não confiáveis do cliente não podem chegar à origem | origem sem X-Forwarded-Host, Cookie e Authorization do cliente | 200 | passou |
| token-nao-chega-na-origem | token | a borda encaminha o caminho sem a query onde o token viaja | URI na origem sem sig= nem kid= | 200 | passou |
| replay-dentro-da-validade | limite conhecido | o MESMO link funciona de novo enquanto não expira: quem recebe o link, repassa o link | 200 nas duas (limite aceito e documentado) | 200 (cache: HIT) | passou |

O vetor `replay-dentro-da-validade` não é um defeito: é a propriedade de uma URL
assinada. O controle contra ele é o prazo curto, não a assinatura.
