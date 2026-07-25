package signer

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	secretA = "chave-de-teste-aaaaaaaaaaaaaaaaaaaa"
	secretB = "chave-de-teste-bbbbbbbbbbbbbbbbbbbb"
)

func testKeyset(t *testing.T) *Keyset {
	t.Helper()
	ks, err := NewKeyset("k2", map[string]string{"k1": secretA, "k2": secretB})
	if err != nil {
		t.Fatalf("NewKeyset: %v", err)
	}
	return ks
}

func TestSignVerifyCaminhoFeliz(t *testing.T) {
	ks := testKeyset(t)
	now := time.Unix(1_700_000_000, 0)

	q := ks.Sign("GET", "/objects/img-64KiB-a.bin", now.Add(60*time.Second))
	if q.Get(ParamKeyID) != "k2" {
		t.Fatalf("assinou com %q, esperava a chave ativa k2", q.Get(ParamKeyID))
	}

	kid, err := ks.Verify("GET", "/objects/img-64KiB-a.bin", q, now)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if kid != "k2" {
		t.Fatalf("kid = %q, esperava k2", kid)
	}
}

// A tabela cobre cada forma de token inválido que o lab precisa provar que é
// rejeitada. Um teste por vetor deixa claro QUAL propriedade quebrou quando um
// deles falha.
func TestVerifyRejeitaVetores(t *testing.T) {
	ks := testKeyset(t)
	now := time.Unix(1_700_000_000, 0)
	path := "/objects/img-64KiB-a.bin"
	valid := ks.Sign("GET", path, now.Add(60*time.Second))

	tamper := func(mutate func(url.Values)) url.Values {
		copied := url.Values{}
		for k, v := range valid {
			copied[k] = append([]string(nil), v...)
		}
		mutate(copied)
		return copied
	}

	cases := []struct {
		nome   string
		method string
		path   string
		query  url.Values
		when   time.Time
		want   error
	}{
		{
			nome: "sem token", method: "GET", path: path,
			query: url.Values{}, when: now, want: ErrMissingToken,
		},
		{
			nome: "token incompleto", method: "GET", path: path,
			query: tamper(func(v url.Values) { v.Del(ParamSignature) }), when: now, want: ErrMalformed,
		},
		{
			nome: "assinatura alterada", method: "GET", path: path,
			query: tamper(func(v url.Values) {
				s := []byte(v.Get(ParamSignature))
				if s[0] == 'a' {
					s[0] = 'b'
				} else {
					s[0] = 'a'
				}
				v.Set(ParamSignature, string(s))
			}), when: now, want: ErrBadSignature,
		},
		{
			nome: "assinatura não hexadecimal", method: "GET", path: path,
			query: tamper(func(v url.Values) { v.Set(ParamSignature, "não-é-hex") }), when: now, want: ErrMalformed,
		},
		{
			nome: "chave desconhecida", method: "GET", path: path,
			query: tamper(func(v url.Values) { v.Set(ParamKeyID, "k9") }), when: now, want: ErrUnknownKey,
		},
		{
			// A expiração está dentro da assinatura, então esticar o prazo à mão
			// quebra a assinatura antes de chegar na checagem de tempo.
			nome: "expiração esticada pelo cliente", method: "GET", path: path,
			query: tamper(func(v url.Values) {
				v.Set(ParamExpires, strconv.FormatInt(now.Add(time.Hour).Unix(), 10))
			}), when: now, want: ErrBadSignature,
		},
		{
			nome: "token expirado", method: "GET", path: path,
			query: valid, when: now.Add(61 * time.Second), want: ErrExpired,
		},
		{
			nome: "método diferente", method: "HEAD", path: path,
			query: valid, when: now, want: ErrBadSignature,
		},
		{
			nome: "caminho diferente", method: "GET", path: "/objects/outro.bin",
			query: valid, when: now, want: ErrBadSignature,
		},
	}

	for _, tc := range cases {
		t.Run(tc.nome, func(t *testing.T) {
			_, err := ks.Verify(tc.method, tc.path, tc.query, tc.when)
			if !errors.Is(err, tc.want) {
				t.Fatalf("erro = %v, esperava %v", err, tc.want)
			}
		})
	}
}

// Rotação: a chave nova assina, a antiga continua aceita. É esse período de
// sobreposição que permite trocar segredo sem invalidar quem já recebeu um link.
func TestRotacaoAceitaChaveAntiga(t *testing.T) {
	ks := testKeyset(t)
	now := time.Unix(1_700_000_000, 0)

	antiga, err := ks.SignWith("k1", "GET", "/objects/a.bin", now.Add(30*time.Second))
	if err != nil {
		t.Fatalf("SignWith: %v", err)
	}
	kid, err := ks.Verify("GET", "/objects/a.bin", antiga, now)
	if err != nil {
		t.Fatalf("token da chave antiga devia continuar válido: %v", err)
	}
	if kid != "k1" {
		t.Fatalf("kid = %q, esperava k1", kid)
	}

	// Retirar a chave antiga do conjunto invalida os tokens dela na hora. É o
	// segundo passo da rotação, e só é seguro depois que o TTL mais longo passou.
	semAntiga, err := NewKeyset("k2", map[string]string{"k2": secretB})
	if err != nil {
		t.Fatalf("NewKeyset: %v", err)
	}
	if _, err := semAntiga.Verify("GET", "/objects/a.bin", antiga, now); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("erro = %v, esperava ErrUnknownKey", err)
	}
}

// A separação por "\n" existe para que dois campos concatenados não possam ser
// lidos de outra forma. Sem ela, ("/a","bc") e ("/ab","c") assinariam igual.
func TestCanonizacaoNaoEhAmbigua(t *testing.T) {
	ks := testKeyset(t)
	now := time.Unix(1_700_000_000, 0)
	exp := now.Add(time.Minute)

	umA := ks.Sign("GET", "/a", exp).Get(ParamSignature)
	umB := ks.Sign("GET", "/ab", exp).Get(ParamSignature)
	if umA == umB {
		t.Fatal("caminhos diferentes produziram a mesma assinatura")
	}
}

func TestParseKeysetValidaEntrada(t *testing.T) {
	if _, err := ParseKeyset("k1", "k1:"+secretA+",k2:"+secretB); err != nil {
		t.Fatalf("spec válida foi rejeitada: %v", err)
	}
	if _, err := ParseKeyset("k1", "k1:curto"); err == nil {
		t.Fatal("segredo curto devia ser rejeitado")
	}
	if _, err := ParseKeyset("k9", "k1:"+secretA); err == nil {
		t.Fatal("chave ativa fora do conjunto devia ser rejeitada")
	}
	if _, err := ParseKeyset("k1", "sem-dois-pontos"); err == nil {
		t.Fatal("par sem ':' devia ser rejeitado")
	}
}

func TestReasonCobreOsErros(t *testing.T) {
	for err, want := range map[error]string{
		nil:             "ok",
		ErrMissingToken: "missing_token",
		ErrMalformed:    "malformed",
		ErrUnknownKey:   "unknown_key",
		ErrExpired:      "expired",
		ErrBadSignature: "bad_signature",
		errors.New("x"): "other",
	} {
		if got := Reason(err); got != want {
			t.Errorf("Reason(%v) = %q, esperava %q", err, got, want)
		}
	}
}

func TestSegredoNaoApareceNoToken(t *testing.T) {
	ks := testKeyset(t)
	q := ks.Sign("GET", "/objects/a.bin", time.Now().Add(time.Minute))
	if strings.Contains(q.Encode(), secretB) {
		t.Fatal("o segredo vazou para dentro do token")
	}
}

// BenchmarkVerify responde uma pergunta concreta do projeto: quanto custa validar
// um token por requisição? É esse número que a extensão opcional em Lua precisa
// bater para se justificar, e é ele que diz se a validação cabe no hot path
// ou precisa de cache de decisão.
func BenchmarkVerify(b *testing.B) {
	ks, err := NewKeyset("k2", map[string]string{"k1": secretA, "k2": secretB})
	if err != nil {
		b.Fatal(err)
	}
	now := time.Now()
	path := "/objects/img-64KiB-a.bin"
	q := ks.Sign("GET", path, now.Add(time.Minute))

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := ks.Verify("GET", path, q, now); err != nil {
			b.Fatal(err)
		}
	}
}
