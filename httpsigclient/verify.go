package httpsigclient

import (
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-fed/httpsig"
)

// ErrNoSignature は Signature ヘッダが無いことを表す。呼び出し側が
// 「署名されていない」と「署名が壊れている」を区別できるようにしてある。
var ErrNoSignature = errors.New("request has no http signature")

// MaxClockSkew は許容する Date のずれ。リプレイの窓を狭めるためで、
// Mastodon も同程度の値を使う。
const MaxClockSkew = 5 * time.Minute

// KeyIDFromRequest は Signature ヘッダから keyId を取り出す。公開鍵を
// どこから引くか (キャッシュか actor 取得か) は呼び出し側が決めるため、
// 検証本体とは分けてある。
func KeyIDFromRequest(r *http.Request) (string, error) {
	if r.Header.Get("Signature") == "" && r.Header.Get("Authorization") == "" {
		return "", ErrNoSignature
	}
	v, err := httpsig.NewVerifier(r)
	if err != nil {
		return "", fmt.Errorf("malformed signature: %w", err)
	}
	keyID := v.KeyId()
	if keyID == "" {
		return "", errors.New("signature has no keyId")
	}
	return keyID, nil
}

// Verify は署名・Digest・Date をまとめて検証する。
//
// go-fed/httpsig の Verifier は「Digest ヘッダが署名対象に含まれていた」
// ことしか確認せず、Digest の値が本文のハッシュと一致するかは見ない。
// そのため本文の照合はここで行う必要がある。これを省くと、正しい署名を
// 使い回して本文だけ差し替える改竄が通ってしまう。
func Verify(r *http.Request, body []byte, publicKeyPEM string) error {
	pub, err := ParsePublicKey(publicKeyPEM)
	if err != nil {
		return err
	}
	if r.Header.Get("Signature") == "" && r.Header.Get("Authorization") == "" {
		return ErrNoSignature
	}
	v, err := httpsig.NewVerifier(r)
	if err != nil {
		return fmt.Errorf("malformed signature: %w", err)
	}
	if err := v.Verify(pub, httpsig.RSA_SHA256); err != nil {
		return fmt.Errorf("signature verification failed: %w", err)
	}
	if err := verifyDigest(r, body); err != nil {
		return err
	}
	return verifyDate(r)
}

func verifyDigest(r *http.Request, body []byte) error {
	got := r.Header.Get("Digest")
	if got == "" {
		// 本文があるのに Digest が無ければ、本文は署名で一切保護されて
		// いない。署名だけ通しても意味が無いので拒否する。
		if len(body) > 0 {
			return errors.New("request has a body but no Digest header")
		}
		return nil
	}
	sum := sha256.Sum256(body)
	want := base64.StdEncoding.EncodeToString(sum[:])
	// Mastodon は "SHA-256=..." を送る。複数アルゴリズムがカンマ区切りで
	// 並ぶこともある。base64 にカンマは現れないので分割して良い。
	for _, part := range strings.Split(got, ",") {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(name), "SHA-256") {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(value)), []byte(want)) != 1 {
			return errors.New("digest does not match the body")
		}
		return nil
	}
	return fmt.Errorf("digest header %q has no SHA-256 entry", got)
}

func verifyDate(r *http.Request) error {
	raw := r.Header.Get("Date")
	if raw == "" {
		return errors.New("request has no Date header")
	}
	t, err := http.ParseTime(raw)
	if err != nil {
		return fmt.Errorf("bad Date header %q: %w", raw, err)
	}
	if skew := time.Since(t); skew > MaxClockSkew || skew < -MaxClockSkew {
		return fmt.Errorf("date %q is outside the allowed clock skew", raw)
	}
	return nil
}

// ParsePublicKey は actor の publicKeyPem を読む。ほとんどの実装は PKIX
// (BEGIN PUBLIC KEY) を出すが、PKCS#1 (BEGIN RSA PUBLIC KEY) を出すものも
// あるので両方受ける。
func ParsePublicKey(publicKeyPEM string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(publicKeyPEM))
	if block == nil {
		return nil, errors.New("public key is not valid PEM")
	}
	if parsed, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		pub, ok := parsed.(*rsa.PublicKey)
		if !ok {
			return nil, fmt.Errorf("public key must be RSA, got %T", parsed)
		}
		return pub, nil
	}
	pub, err := x509.ParsePKCS1PublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("public key is neither PKIX nor PKCS#1: %w", err)
	}
	return pub, nil
}
