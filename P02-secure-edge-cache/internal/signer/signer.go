// Package signer emite e valida URLs assinadas com HMAC.
//
// O problema que ele resolve: a borda quer guardar UMA cópia de um objeto e
// entregá-la a muita gente, mas nem todo mundo tem direito àquele objeto. Se a
// autorização virasse parte da chave de cache, cada usuário teria sua própria
// cópia e o cache perderia o sentido. Se a autorização não existisse, qualquer um
// baixaria qualquer coisa.
//
// A URL assinada separa as duas coisas. O token viaja na query string, é
// verificado a cada requisição e NÃO entra na chave de cache. O objeto guardado é
// o mesmo para todos; o direito de recebê-lo é verificado por requisição.
//
// O que a assinatura cobre:
//
//	v1 \n método \n caminho \n expiração \n identificador da chave
//
// Cada campo está lá por um motivo. Sem o método, um token de GET autorizaria
// DELETE. Sem o caminho, um token de um objeto abriria todos. Sem a expiração,
// um link vazado valeria para sempre. Sem o identificador da chave, trocar a
// chave em uso exigiria invalidar todos os tokens de uma vez.
package signer

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Nomes dos parâmetros do token na query string.
const (
	ParamKeyID     = "kid"
	ParamExpires   = "exp"
	ParamSignature = "sig"

	// scheme versiona o formato canônico. Sem isso, mudar o que entra na
	// assinatura no futuro tornaria tokens antigos e novos indistinguíveis, e a
	// migração viraria adivinhação.
	scheme = "v1"
)

// Erros de validação. Eles são separados porque cada um vira um rótulo de
// métrica diferente: "expirado" é operação normal, "assinatura inválida" pode
// ser ataque, e confundir os dois esconde o segundo dentro do primeiro.
var (
	ErrMissingToken = errors.New("token ausente")
	ErrMalformed    = errors.New("token malformado")
	ErrUnknownKey   = errors.New("identificador de chave desconhecido")
	ErrExpired      = errors.New("token expirado")
	ErrBadSignature = errors.New("assinatura inválida")
)

// Keyset guarda as chaves aceitas e qual delas assina agora.
//
// Duas chaves ativas ao mesmo tempo é o que torna a rotação possível sem
// derrubar ninguém: a nova chave passa a assinar, a antiga continua sendo aceita
// pelo tempo de vida do token mais longo em circulação, e só então é removida.
type Keyset struct {
	active string
	keys   map[string][]byte
}

// NewKeyset monta o conjunto a partir de segredos já separados.
func NewKeyset(active string, keys map[string]string) (*Keyset, error) {
	if len(keys) == 0 {
		return nil, errors.New("nenhuma chave fornecida")
	}
	ks := &Keyset{active: active, keys: make(map[string][]byte, len(keys))}
	for id, secret := range keys {
		if id == "" {
			return nil, errors.New("identificador de chave vazio")
		}
		if strings.ContainsAny(id, ":,") {
			return nil, fmt.Errorf("identificador %q não pode conter ':' nem ','", id)
		}
		// 16 bytes é o piso razoável para um segredo de HMAC. Abaixo disso a
		// chave vira o elo fraco e não adianta o algoritmo ser bom.
		if len(secret) < 16 {
			return nil, fmt.Errorf("segredo da chave %q é curto demais (mínimo 16 bytes)", id)
		}
		ks.keys[id] = []byte(secret)
	}
	if _, ok := ks.keys[active]; !ok {
		return nil, fmt.Errorf("chave ativa %q não está no conjunto", active)
	}
	return ks, nil
}

// ParseKeyset lê o formato "id1:segredo1,id2:segredo2".
//
// Esse formato existe para o segredo chegar por variável de ambiente e nunca
// por arquivo versionado. O repositório guarda .env.example com instruções de
// como gerar os valores, nunca os valores.
func ParseKeyset(active, spec string) (*Keyset, error) {
	keys := map[string]string{}
	for _, pair := range strings.Split(spec, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		id, secret, ok := strings.Cut(pair, ":")
		if !ok {
			return nil, fmt.Errorf("par inválido %q: use id:segredo", id)
		}
		keys[strings.TrimSpace(id)] = strings.TrimSpace(secret)
	}
	return NewKeyset(active, keys)
}

// ActiveKeyID devolve a chave que está assinando agora.
func (k *Keyset) ActiveKeyID() string { return k.active }

// KeyIDs lista os identificadores aceitos, em ordem estável.
func (k *Keyset) KeyIDs() []string {
	ids := make([]string, 0, len(k.keys))
	for id := range k.keys {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Sign devolve os parâmetros do token para um método e caminho.
func (k *Keyset) Sign(method, path string, expires time.Time) url.Values {
	values, err := k.SignWith(k.active, method, path, expires)
	if err != nil {
		// Só acontece se a chave ativa sumir do mapa, o que o construtor impede.
		panic(err)
	}
	return values
}

// SignWith assina com uma chave específica. Serve para testar rotação: dá para
// emitir com a chave antiga e conferir que a borda ainda aceita.
func (k *Keyset) SignWith(kid, method, path string, expires time.Time) (url.Values, error) {
	secret, ok := k.keys[kid]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownKey, kid)
	}
	exp := strconv.FormatInt(expires.Unix(), 10)
	values := url.Values{}
	values.Set(ParamKeyID, kid)
	values.Set(ParamExpires, exp)
	values.Set(ParamSignature, sign(secret, method, path, exp, kid))
	return values, nil
}

// Verify confere um token e devolve o identificador da chave que o validou.
//
// A ordem das checagens importa. A assinatura é conferida ANTES da expiração
// porque, até a assinatura fechar, nenhum campo do token é confiável: rejeitar
// por "expirado" um valor de exp que qualquer um pode escrever seria acreditar
// em dado não autenticado.
func (k *Keyset) Verify(method, path string, q url.Values, now time.Time) (string, error) {
	kid := q.Get(ParamKeyID)
	exp := q.Get(ParamExpires)
	sig := q.Get(ParamSignature)

	if kid == "" && exp == "" && sig == "" {
		return "", ErrMissingToken
	}
	if kid == "" || exp == "" || sig == "" {
		return "", fmt.Errorf("%w: faltam parâmetros do token", ErrMalformed)
	}

	secret, ok := k.keys[kid]
	if !ok {
		return kid, fmt.Errorf("%w: %q", ErrUnknownKey, kid)
	}

	got, err := hex.DecodeString(sig)
	if err != nil {
		return kid, fmt.Errorf("%w: assinatura não é hexadecimal", ErrMalformed)
	}
	want, err := hex.DecodeString(sign(secret, method, path, exp, kid))
	if err != nil {
		return kid, fmt.Errorf("%w: falha interna ao gerar comparação", ErrMalformed)
	}

	// hmac.Equal compara em tempo constante: o tempo da comparação não depende
	// de QUANTOS bytes bateram. Um `==` comum sai no primeiro byte diferente, e
	// essa diferença de microssegundos, medida muitas vezes, permite descobrir a
	// assinatura correta byte a byte.
	if !hmac.Equal(got, want) {
		return kid, ErrBadSignature
	}

	// Só agora o exp é confiável: ele está coberto pela assinatura que fechou.
	seconds, err := strconv.ParseInt(exp, 10, 64)
	if err != nil {
		return kid, fmt.Errorf("%w: expiração não é um inteiro", ErrMalformed)
	}
	if now.After(time.Unix(seconds, 0)) {
		return kid, ErrExpired
	}
	return kid, nil
}

// sign monta a string canônica e devolve o HMAC em hexadecimal.
//
// Os campos são separados por "\n", um caractere que não aparece em método,
// caminho, timestamp nem identificador de chave. Concatenar sem separador criaria
// ambiguidade: ("/a", "bc") e ("/ab", "c") produziriam a mesma string, e um token
// de um caminho valeria para outro.
func sign(secret []byte, method, path, exp, kid string) string {
	mac := hmac.New(sha256.New, secret)
	for _, field := range []string{scheme, method, path, exp, kid} {
		mac.Write([]byte(field))
		mac.Write([]byte{'\n'})
	}
	return hex.EncodeToString(mac.Sum(nil))
}

// Reason traduz um erro em rótulo de métrica.
//
// Rótulo de métrica precisa ter cardinalidade pequena e fechada. Jogar err.Error()
// direto num label é como criar uma série nova por mensagem, e é assim que se
// derruba um Prometheus sem querer.
func Reason(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, ErrMissingToken):
		return "missing_token"
	case errors.Is(err, ErrMalformed):
		return "malformed"
	case errors.Is(err, ErrUnknownKey):
		return "unknown_key"
	case errors.Is(err, ErrExpired):
		return "expired"
	case errors.Is(err, ErrBadSignature):
		return "bad_signature"
	default:
		return "other"
	}
}
