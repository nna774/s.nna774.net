package httpsigclient

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testKeyID = "https://s.nna774.net/u/nana#main-key"

type captured struct {
	req  *http.Request
	body []byte
}

func publicKeyPEM(t *testing.T, key *rsa.PrivateKey) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

// signAndCapture は署名付きリクエストを実際に HTTP で飛ばし、サーバ側で
// 受け取ったものを返す。ネットワークを一往復させるのが要点で、Go の
// クライアントが Host をどう扱うかも含めて検証できる。
func signAndCapture(t *testing.T, body []byte) (*captured, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	pubPEM := publicKeyPEM(t, key)

	got := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got.req = r
		got.body = b
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	signer, err := NewSigner(key, pubPEM, testKeyID)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	resp, err := signer.RequestWithSign(context.Background(), http.MethodPost, srv.URL+"/u/nana/inbox", body)
	if err != nil {
		t.Fatalf("RequestWithSign: %v", err)
	}
	resp.Body.Close()

	if got.req == nil {
		t.Fatal("server did not receive the request")
	}
	return got, pubPEM
}

// 自分が署名したものを自分で検証できること。署名側は r.Header を見て、
// 検証側は r.Host を Host ヘッダに補うという非対称があるため、Host を
// 署名対象に含めた瞬間にここが壊れやすい。
func TestSignThenVerifyRoundTrip(t *testing.T) {
	got, pubPEM := signAndCapture(t, []byte(`{"type":"Follow"}`))
	if err := Verify(got.req, got.body, pubPEM); err != nil {
		t.Fatalf("Verify of our own signature failed: %v", err)
	}
}

// 署名対象に Host を含めているので、ワイヤ上でも署名文字列でも整合して
// いなければならない。
func TestSignedHeadersIncludeHostAndDigest(t *testing.T) {
	got, _ := signAndCapture(t, []byte(`{}`))
	sig := got.req.Header.Get("Signature")
	for _, want := range []string{`headers="`, "(request-target)", "host", "date", "digest", "content-type"} {
		if !strings.Contains(sig, want) {
			t.Errorf("Signature header does not cover %q: %s", want, sig)
		}
	}
	if got.req.Header.Get("Digest") == "" {
		t.Error("Digest header was not set")
	}
}

func TestKeyIDFromRequest(t *testing.T) {
	got, _ := signAndCapture(t, []byte(`{}`))
	keyID, err := KeyIDFromRequest(got.req)
	if err != nil {
		t.Fatalf("KeyIDFromRequest: %v", err)
	}
	if keyID != testKeyID {
		t.Errorf("keyID = %q, want %q", keyID, testKeyID)
	}
}

// 本文だけ差し替えられたら弾かねばならない。httpsig の Verifier は
// Digest の値と本文の一致を見ないので、この検証を自前で足していなければ
// 通ってしまう。
func TestVerifyRejectsTamperedBody(t *testing.T) {
	got, pubPEM := signAndCapture(t, []byte(`{"type":"Follow"}`))

	err := Verify(got.req, []byte(`{"type":"Delete"}`), pubPEM)
	if err == nil {
		t.Fatal("Verify accepted a tampered body")
	}
	if !strings.Contains(err.Error(), "digest") {
		t.Errorf("error should mention the digest, got: %v", err)
	}
}

func TestVerifyRejectsWrongKey(t *testing.T) {
	got, _ := signAndCapture(t, []byte(`{}`))

	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if err := Verify(got.req, got.body, publicKeyPEM(t, other)); err == nil {
		t.Fatal("Verify accepted a signature made with a different key")
	}
}

func TestVerifyRejectsMissingSignature(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "https://s.nna774.net/u/nana/inbox", strings.NewReader("{}"))
	key, _ := rsa.GenerateKey(rand.Reader, 2048)

	if err := Verify(r, []byte("{}"), publicKeyPEM(t, key)); err != ErrNoSignature {
		t.Errorf("err = %v, want ErrNoSignature", err)
	}
	if _, err := KeyIDFromRequest(r); err != ErrNoSignature {
		t.Errorf("KeyIDFromRequest err = %v, want ErrNoSignature", err)
	}
}

// Date を書き換えたものは弾かねばならない。Date は署名対象に入っている
// ので署名検証の段階で落ちる。
func TestVerifyRejectsRewrittenDate(t *testing.T) {
	got, pubPEM := signAndCapture(t, []byte(`{}`))

	stale := got.req.Clone(context.Background())
	stale.Header.Set("Date", time.Now().Add(-2*time.Hour).UTC().Format(http.TimeFormat))
	if err := Verify(stale, got.body, pubPEM); err == nil {
		t.Fatal("Verify accepted a request with a rewritten Date")
	}
}

func TestVerifyDateBoundary(t *testing.T) {
	for _, tt := range []struct {
		name    string
		date    string
		wantErr bool
	}{
		{"now", CurrentTime(), false},
		{"absent", "", true},
		{"garbage", "yesterday", true},
		{"too old", time.Now().Add(-MaxClockSkew - time.Minute).UTC().Format(http.TimeFormat), true},
		{"too far in the future", time.Now().Add(MaxClockSkew + time.Minute).UTC().Format(http.TimeFormat), true},
		{"slightly in the future is fine", time.Now().Add(time.Minute).UTC().Format(http.TimeFormat), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "https://s.nna774.net/u/nana/inbox", nil)
			if tt.date != "" {
				r.Header.Set("Date", tt.date)
			}
			err := verifyDate(r)
			if tt.wantErr && err == nil {
				t.Error("verifyDate accepted it, want error")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("verifyDate rejected it: %v", err)
			}
		})
	}
}

func TestVerifyDigestRequiresDigestWhenBodyPresent(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "https://s.nna774.net/u/nana/inbox", nil)
	if err := verifyDigest(r, []byte(`{"a":1}`)); err == nil {
		t.Error("a body with no Digest header should be rejected")
	}
	// 本文が無いなら Digest が無くても良い (GET など)。
	if err := verifyDigest(r, nil); err != nil {
		t.Errorf("empty body with no Digest should be fine, got %v", err)
	}
}

func TestParsePublicKey(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)

	t.Run("PKIX", func(t *testing.T) {
		der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
		p := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
		got, err := ParsePublicKey(p)
		if err != nil {
			t.Fatalf("ParsePublicKey: %v", err)
		}
		if !got.Equal(&key.PublicKey) {
			t.Error("parsed key differs")
		}
	})

	t.Run("PKCS#1", func(t *testing.T) {
		der := x509.MarshalPKCS1PublicKey(&key.PublicKey)
		p := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: der}))
		got, err := ParsePublicKey(p)
		if err != nil {
			t.Fatalf("ParsePublicKey: %v", err)
		}
		if !got.Equal(&key.PublicKey) {
			t.Error("parsed key differs")
		}
	})

	t.Run("garbage", func(t *testing.T) {
		if _, err := ParsePublicKey("nope"); err == nil {
			t.Error("ParsePublicKey accepted garbage")
		}
	})
}

// Date ヘッダは RFC1123 で GMT 表記でなければならない。ActivityStreams の
// published と混同すると相互運用性が壊れる。
func TestCurrentTimeIsRFC1123GMT(t *testing.T) {
	got := CurrentTime()
	if !strings.HasSuffix(got, "GMT") {
		t.Errorf("CurrentTime() = %q, should end with GMT", got)
	}
	if _, err := http.ParseTime(got); err != nil {
		t.Errorf("CurrentTime() = %q is not a valid HTTP date: %v", got, err)
	}
}
