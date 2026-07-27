package httpsigclient

import (
	"bytes"
	"context"
	"crypto/rsa"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-fed/httpsig"
	"github.com/nna774/s.nna774.net/activitystream"
)

// CurrentTime は HTTP の Date ヘッダ用の書式を返す。ActivityStreams の
// published は ISO8601 なので、こちらを流用してはならない。
func CurrentTime() string {
	return strings.ReplaceAll(time.Now().UTC().Format(time.RFC1123), "UTC", "GMT")
}

type Signer struct {
	// postSigner は本文があるリクエスト用。Digest を署名対象に含む。
	postSigner httpsig.Signer
	// getSigner は本文が無いリクエスト用。authorized fetch を要求する
	// インスタンスから actor を取るのに使う。Digest は無い。
	getSigner httpsig.Signer
	// httpsig.Signer は goroutine safe ではない。
	mu      sync.Mutex
	privKey *rsa.PrivateKey
	keyID   string
}

const signatureExpiry = int64(time.Minute)

func NewSigner(privKey *rsa.PrivateKey, publicKey string, keyID string) (*Signer, error) {
	postSigner, _, err := httpsig.NewSigner(
		nil, httpsig.DigestSha256,
		[]string{httpsig.RequestTarget, "Host", "Date", "Digest", "Content-Type"},
		httpsig.Signature, signatureExpiry)
	if err != nil {
		return nil, err
	}
	getSigner, _, err := httpsig.NewSigner(
		nil, httpsig.DigestSha256,
		[]string{httpsig.RequestTarget, "Host", "Date"},
		httpsig.Signature, signatureExpiry)
	if err != nil {
		return nil, err
	}
	return &Signer{
		postSigner: postSigner,
		getSigner:  getSigner,
		privKey:    privKey,
		keyID:      keyID,
	}, nil
}

// KeyID は署名に使う keyId を返す。
func (s *Signer) KeyID() string { return s.keyID }

// RequestWithSign は本文付きのリクエストに署名して送る。
func (s *Signer) RequestWithSign(ctx context.Context, method string, url string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewBuffer(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("content-type", activitystream.ContentType)
	req.Header.Set("date", CurrentTime())
	req.Header.Set("accept", activitystream.ActivityStreamsContentType)
	if err := s.sign(s.postSigner, req, body); err != nil {
		return nil, err
	}
	return http.DefaultClient.Do(req)
}

// GetWithSign は本文の無い GET に署名して送る。authorized fetch を
// 有効にしているインスタンスは、署名の無い actor 取得を拒否する。
func (s *Signer) GetWithSign(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("date", CurrentTime())
	req.Header.Set("accept", activitystream.ActivityStreamsContentType)
	// body に nil を渡すと httpsig は Digest を付けない。
	if err := s.sign(s.getSigner, req, nil); err != nil {
		return nil, err
	}
	return http.DefaultClient.Do(req)
}

func (s *Signer) sign(signer httpsig.Signer, req *http.Request, body []byte) error {
	// httpsig の署名側は r.Header だけを見て署名文字列を作り、検証側
	// (NewVerifier) は r.Host を Host ヘッダに補う。Go のクライアントは
	// Host を r.Header ではなく r.Host に持つため、明示的に入れておかないと
	// 署名側と検証側で文字列が食い違って必ず検証が落ちる。
	// なお Header["Host"] はクライアント送信時には無視されるので、
	// ワイヤ上のヘッダが二重になることはない。
	if req.Header.Get("Host") == "" {
		host := req.Host
		if host == "" {
			host = req.URL.Host
		}
		req.Header.Set("Host", host)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return signer.SignRequest(s.privKey, s.keyID, req, body)
}
