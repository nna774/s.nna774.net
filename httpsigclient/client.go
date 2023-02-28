package httpsigclient

import (
	"bytes"
	"crypto/rsa"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-fed/httpsig"
	"github.com/nna774/s.nna774.net/activitystream"
)

func CurrentTime() string {
	return strings.ReplaceAll(time.Now().UTC().Format(time.RFC1123), "UTC", "GMT")
}

type Signer struct {
	signer    httpsig.Signer
	mu        *sync.Mutex
	privKey   *rsa.PrivateKey
	publicKey string
	keyID     string
}

func NewSigner(privKey *rsa.PrivateKey, publicKey string, keyID string) (*Signer, error) {
	signer, _, err := httpsig.NewSigner(nil, httpsig.DigestSha256, []string{httpsig.RequestTarget, "Date", "Digest"}, httpsig.Signature, int64(time.Minute))
	if err != nil {
		return nil, err
	}
	return &Signer{
		signer:    signer,
		mu:        &sync.Mutex{},
		privKey:   privKey,
		publicKey: publicKey,
		keyID:     keyID,
	}, nil
}

func (s *Signer) RequestWithSign(method string, url string, body []byte) (*http.Response, error) {
	req, err := http.NewRequest(method, url, bytes.NewBuffer(body))
	if err != nil {
		return nil, err
	}
	req.Header.Add("content-type", activitystream.ActivityStreamsContentType)
	req.Header.Add("date", CurrentTime())
	s.mu.Lock()
	defer s.mu.Unlock()
	err = s.signer.SignRequest(s.privKey, s.keyID, req, body)
	if err != nil {
		return nil, err
	}
	return http.DefaultClient.Do(req)
}
