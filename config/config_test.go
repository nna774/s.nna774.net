package config

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"testing"
)

func newKeyPEM(t *testing.T, pkcs8 bool) ([]byte, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	var block pem.Block
	if pkcs8 {
		der, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
		}
		block = pem.Block{Type: "PRIVATE KEY", Bytes: der}
	} else {
		block = pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}
	}
	return pem.EncodeToMemory(&block), key
}

// 既存の鍵は PKCS#1 だが OpenSSL 3 の genrsa は PKCS#8 を出すため、
// 鍵を作り直したときに読めなくなってはならない。
func TestSetPrivateKeyAcceptsBothPEMFormats(t *testing.T) {
	for _, tt := range []struct {
		name  string
		pkcs8 bool
	}{
		{"PKCS#1", false},
		{"PKCS#8", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			buf, want := newKeyPEM(t, tt.pkcs8)
			a := &ActorConfig{}
			if err := a.setPrivateKey(buf); err != nil {
				t.Fatalf("setPrivateKey: %v", err)
			}
			if !a.PrivateKey().Equal(want) {
				t.Error("loaded private key differs from the original")
			}
		})
	}
}

// actor に載せる公開鍵は秘密鍵から導出する。導出結果が元の鍵の公開鍵と
// 一致しないと、リモート側での署名検証が通らない。
func TestPublicKeyIsDerivedFromPrivateKey(t *testing.T) {
	buf, key := newKeyPEM(t, false)
	a := &ActorConfig{}
	if err := a.setPrivateKey(buf); err != nil {
		t.Fatalf("setPrivateKey: %v", err)
	}

	block, _ := pem.Decode([]byte(a.PublicKey()))
	if block == nil {
		t.Fatal("PublicKey() is not valid PEM")
	}
	if block.Type != "PUBLIC KEY" {
		t.Errorf("PEM type = %q, want %q", block.Type, "PUBLIC KEY")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatalf("ParsePKIXPublicKey: %v", err)
	}
	if !pub.(*rsa.PublicKey).Equal(&key.PublicKey) {
		t.Error("derived public key does not match the private key")
	}
}

func TestSetPrivateKeyRejectsGarbage(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   string
	}{
		{"not PEM", "hello"},
		{"empty", ""},
		{"PEM with broken DER", string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte("nope")}))},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a := &ActorConfig{}
			if err := a.setPrivateKey([]byte(tt.in)); err == nil {
				t.Error("setPrivateKey succeeded, want error")
			}
		})
	}
}

func TestIconMediaType(t *testing.T) {
	for _, tt := range []struct {
		uri  string
		want string
	}{
		{"https://nna774.net/img/1012_filtered.jpg", "image/jpeg"},
		{"https://example.com/a.png", "image/png"},
		{"https://example.com/a.gif", "image/gif"},
		{"https://example.com/noext", "application/octet-stream"},
	} {
		t.Run(tt.uri, func(t *testing.T) {
			got := (&ActorConfig{IconURI: tt.uri}).IconMediaType()
			// mime.TypeByExtension は charset 付きで返すことがあるため前方一致で見る。
			if len(got) < len(tt.want) || got[:len(tt.want)] != tt.want {
				t.Errorf("IconMediaType() = %q, want prefix %q", got, tt.want)
			}
		})
	}
}

func TestLocalPartAndID(t *testing.T) {
	a := &ActorConfig{Username: "nana@s.nna774.net", Origin: "https://s.nna774.net"}
	if got, want := a.LocalPart(), "nana"; got != want {
		t.Errorf("LocalPart() = %q, want %q", got, want)
	}
	if got, want := a.ID(), "https://s.nna774.net/u/nana"; got != want {
		t.Errorf("ID() = %q, want %q", got, want)
	}
}

func TestValidateRequiresExactlyOnePrimary(t *testing.T) {
	base := func(primary bool, localpart string) *ActorConfig {
		return &ActorConfig{Username: localpart + "@example.com", Primary: primary, ActorType: ActorTypePerson}
	}
	for _, tt := range []struct {
		name    string
		actors  []*ActorConfig
		wantErr bool
	}{
		{"no actors", nil, true},
		{"no primary", []*ActorConfig{base(false, "a")}, true},
		{"one primary", []*ActorConfig{base(true, "a")}, false},
		{"two primaries", []*ActorConfig{base(true, "a"), base(true, "b")}, true},
		{"duplicate localpart", []*ActorConfig{base(true, "a"), base(false, "a")}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := &Config{Actors: tt.actors}
			err := c.validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("validate() err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateRejectsBadActorType(t *testing.T) {
	c := &Config{Actors: []*ActorConfig{
		{Username: "a@example.com", Primary: true, ActorType: "Group"},
	}}
	if err := c.validate(); err == nil {
		t.Error("validate() succeeded, want error for invalid actor_type")
	}
}
