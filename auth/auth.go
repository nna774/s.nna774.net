// Package auth は私用エンドポイント (投稿・タイムライン閲覧・フォロー)
// の認証を担う。
//
// ブラウザと curl の両方から使うため経路を2つ用意する。
//
//   - curl: Authorization: Bearer <token>
//   - ブラウザ: POST /login で発行した HMAC 署名 Cookie
//
// Cookie は自己検証できるためセッションストアを持たない。失効させたい
// ときは署名鍵を差し替えれば全セッションが一括で無効になる。
//
// 重要: このパッケージを ActivityPub のエンドポイント (actor / inbox /
// outbox / .well-known) に掛けてはならない。連合が黙って壊れる。
// inbox は HTTP Signature という別系統で守る。
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// SessionCookieName は Cookie の名前。
const SessionCookieName = "s"

// DefaultSessionTTL はログインの有効期間。
const DefaultSessionTTL = 30 * 24 * time.Hour

var (
	ErrUnauthenticated = errors.New("unauthenticated")
	ErrCrossSite       = errors.New("cross-site request rejected")
)

type Authenticator struct {
	token         []byte
	sessionSecret []byte
	ttl           time.Duration
	// secure は Cookie に Secure 属性を付けるか。localhost での開発では
	// HTTPS でないため付けられない。
	secure bool
}

func New(token, sessionSecret string, secure bool) (*Authenticator, error) {
	if token == "" {
		return nil, errors.New("auth: token must not be empty")
	}
	if sessionSecret == "" {
		return nil, errors.New("auth: session secret must not be empty")
	}
	return &Authenticator{
		token:         []byte(token),
		sessionSecret: []byte(sessionSecret),
		ttl:           DefaultSessionTTL,
		secure:        secure,
	}, nil
}

// CheckToken は与えられたトークンが正しいかを一定時間で比較する。
func (a *Authenticator) CheckToken(token string) bool {
	return subtle.ConstantTimeCompare([]byte(token), a.token) == 1
}

// Authenticated は Bearer か Cookie のどちらかで認証できるかを返す。
//
// viaBearer は Bearer 経路で通ったかを示す。Bearer はブラウザが自動
// 付与しないため CSRF が成立しない。呼び出し側は Cookie 経路のときだけ
// Sec-Fetch-Site を見れば良い。
func (a *Authenticator) Authenticated(r *http.Request) (ok bool, viaBearer bool) {
	if token, found := bearerToken(r); found {
		return a.CheckToken(token), true
	}
	c, err := r.Cookie(SessionCookieName)
	if err != nil {
		return false, false
	}
	return a.validSession(c.Value), false
}

// Authorize は認証と、状態を変える操作に対する CSRF 対策をまとめて行う。
// mutating が true のとき Cookie 経路では同一オリジンからの要求である
// ことを要求する。
func (a *Authenticator) Authorize(r *http.Request, mutating bool) error {
	ok, viaBearer := a.Authenticated(r)
	if !ok {
		return ErrUnauthenticated
	}
	if mutating && !viaBearer && !sameSiteRequest(r) {
		return ErrCrossSite
	}
	return nil
}

func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", false
	}
	const prefix = "bearer "
	if len(h) < len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	return strings.TrimSpace(h[len(prefix):]), true
}

// sameSiteRequest は Sec-Fetch-Site を見て同一オリジン由来かを判定する。
//
// このヘッダは全てのモダンブラウザが強制的に付け、JavaScript から
// 偽装できないため CSRF 対策として堅い。Cookie の SameSite=Strict と
// 二重にする。ヘッダが無い場合 (古いブラウザや curl) は Cookie 経路では
// 許可しない。curl なら Bearer を使えば良い。
func sameSiteRequest(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		// none はアドレスバー直打ちなど、ユーザ自身の操作による遷移。
		return true
	default:
		return false
	}
}

// NewSessionCookie はログイン成功後に渡す Cookie を作る。
func (a *Authenticator) NewSessionCookie() *http.Cookie {
	exp := time.Now().Add(a.ttl)
	return &http.Cookie{
		Name:  SessionCookieName,
		Value: a.signSession(exp.Unix()),
		Path:  "/",
		// HttpOnly で JavaScript から読めなくする。
		HttpOnly: true,
		Secure:   a.secure,
		// SameSite=Strict でクロスサイトからの遷移では送られなくなる。
		SameSite: http.SameSiteStrictMode,
		Expires:  exp,
		MaxAge:   int(a.ttl.Seconds()),
	}
}

// ClearSessionCookie はログアウト用に Cookie を潰す。
func (a *Authenticator) ClearSessionCookie() *http.Cookie {
	return &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   a.secure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	}
}

// signSession は "<有効期限>.<HMAC>" を作る。セッションストアを持たずに
// 自己検証できる。
func (a *Authenticator) signSession(exp int64) string {
	payload := strconv.FormatInt(exp, 10)
	return payload + "." + a.mac(payload)
}

func (a *Authenticator) validSession(value string) bool {
	payload, sig, ok := strings.Cut(value, ".")
	if !ok {
		return false
	}
	// 先に MAC を検証する。中身を信用してから期限を見る。
	if subtle.ConstantTimeCompare([]byte(sig), []byte(a.mac(payload))) != 1 {
		return false
	}
	exp, err := strconv.ParseInt(payload, 10, 64)
	if err != nil {
		return false
	}
	return time.Now().Unix() < exp
}

func (a *Authenticator) mac(payload string) string {
	m := hmac.New(sha256.New, a.sessionSecret)
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// WantsHTML はブラウザからの要求かを判定する。認証が無いときに 401 を
// 返すかログイン画面へ誘導するかの分岐に使う。
func WantsHTML(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

func (a *Authenticator) String() string {
	return fmt.Sprintf("auth.Authenticator(ttl=%v, secure=%v)", a.ttl, a.secure)
}
