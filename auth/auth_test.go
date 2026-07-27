package auth

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func newAuth(t *testing.T) *Authenticator {
	t.Helper()
	a, err := New("s3cret-token", "session-hmac-key", true)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

func TestNewRejectsEmptySecrets(t *testing.T) {
	if _, err := New("", "x", true); err == nil {
		t.Error("New accepted an empty token")
	}
	if _, err := New("x", "", true); err == nil {
		t.Error("New accepted an empty session secret")
	}
}

func TestCheckToken(t *testing.T) {
	a := newAuth(t)
	if !a.CheckToken("s3cret-token") {
		t.Error("the correct token was rejected")
	}
	for _, bad := range []string{"", "s3cret-toke", "s3cret-tokenn", "S3CRET-TOKEN", "wrong"} {
		if a.CheckToken(bad) {
			t.Errorf("token %q was accepted", bad)
		}
	}
}

func TestBearerAuth(t *testing.T) {
	a := newAuth(t)
	for _, tt := range []struct {
		name   string
		header string
		want   bool
	}{
		{"correct", "Bearer s3cret-token", true},
		{"lowercase scheme", "bearer s3cret-token", true},
		{"extra spaces", "Bearer   s3cret-token  ", true},
		{"wrong token", "Bearer nope", false},
		{"no scheme", "s3cret-token", false},
		{"basic", "Basic czNjcmV0", false},
		{"empty", "", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/timeline", nil)
			if tt.header != "" {
				r.Header.Set("Authorization", tt.header)
			}
			ok, viaBearer := a.Authenticated(r)
			if ok != tt.want {
				t.Errorf("Authenticated() = %v, want %v", ok, tt.want)
			}
			if tt.header != "" && strings.HasPrefix(strings.ToLower(tt.header), "bearer ") && !viaBearer {
				t.Error("viaBearer should be true for a Bearer header")
			}
		})
	}
}

func TestSessionCookieRoundTrip(t *testing.T) {
	a := newAuth(t)
	c := a.NewSessionCookie()

	if !c.HttpOnly {
		t.Error("cookie must be HttpOnly")
	}
	if !c.Secure {
		t.Error("cookie must be Secure")
	}
	if c.SameSite != http.SameSiteStrictMode {
		t.Errorf("SameSite = %v, want Strict", c.SameSite)
	}

	r := httptest.NewRequest(http.MethodGet, "/timeline", nil)
	r.AddCookie(c)
	ok, viaBearer := a.Authenticated(r)
	if !ok {
		t.Error("a freshly issued cookie was rejected")
	}
	if viaBearer {
		t.Error("viaBearer should be false for the cookie path")
	}
}

// HMAC を1バイトでも書き換えたものは通ってはならない。
func TestTamperedCookieIsRejected(t *testing.T) {
	a := newAuth(t)
	valid := a.NewSessionCookie().Value
	payload, sig, _ := strings.Cut(valid, ".")

	tampered := []string{
		payload + "." + flipLastByte(sig),
		payload + ".",
		payload,
		"." + sig,
		// 期限だけ未来に伸ばしても MAC が合わない。
		strconv.FormatInt(time.Now().Add(9999*time.Hour).Unix(), 10) + "." + sig,
		"garbage.garbage",
		"",
	}
	for _, v := range tampered {
		t.Run(v, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/timeline", nil)
			r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: v})
			if ok, _ := a.Authenticated(r); ok {
				t.Errorf("tampered cookie %q was accepted", v)
			}
		})
	}
}

// 期限切れの Cookie は、署名が正しくても通してはならない。
func TestExpiredCookieIsRejected(t *testing.T) {
	a := newAuth(t)
	expired := a.signSession(time.Now().Add(-time.Hour).Unix())

	r := httptest.NewRequest(http.MethodGet, "/timeline", nil)
	r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: expired})
	if ok, _ := a.Authenticated(r); ok {
		t.Error("an expired cookie was accepted")
	}
}

// 署名鍵を差し替えると既存の Cookie が全て無効になること。トークンを
// 変えずにセッションだけ失効させる手段として使う。
func TestRotatingSessionSecretInvalidatesCookies(t *testing.T) {
	a := newAuth(t)
	cookie := a.NewSessionCookie()

	rotated, err := New("s3cret-token", "a-different-session-key", true)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	r := httptest.NewRequest(http.MethodGet, "/timeline", nil)
	r.AddCookie(cookie)
	if ok, _ := rotated.Authenticated(r); ok {
		t.Error("a cookie signed with the old secret still works")
	}
	// トークンの方は据え置きで通る。
	r2 := httptest.NewRequest(http.MethodGet, "/timeline", nil)
	r2.Header.Set("Authorization", "Bearer s3cret-token")
	if ok, _ := rotated.Authenticated(r2); !ok {
		t.Error("the token should be unaffected by rotating the session secret")
	}
}

// これが CSRF 対策の本体。別オリジンのフォームから投稿させられない。
func TestAuthorizeRejectsCrossSiteMutation(t *testing.T) {
	a := newAuth(t)
	cookie := a.NewSessionCookie()

	for _, tt := range []struct {
		fetchSite string
		mutating  bool
		wantErr   error
	}{
		{"same-origin", true, nil},
		{"none", true, nil},
		{"cross-site", true, ErrCrossSite},
		{"same-site", true, ErrCrossSite},
		{"", true, ErrCrossSite},
		// 読み取りだけなら Sec-Fetch-Site を要求しない。
		{"cross-site", false, nil},
		{"", false, nil},
	} {
		name := tt.fetchSite
		if name == "" {
			name = "(absent)"
		}
		t.Run(name+"/mutating="+strconv.FormatBool(tt.mutating), func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/u/nana/statuses", nil)
			r.AddCookie(cookie)
			if tt.fetchSite != "" {
				r.Header.Set("Sec-Fetch-Site", tt.fetchSite)
			}
			if err := a.Authorize(r, tt.mutating); err != tt.wantErr {
				t.Errorf("Authorize() = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

// Bearer 経路には Sec-Fetch-Site を要求しない。ブラウザが自動付与しない
// ので CSRF が成立しないため。curl から使える必要がある。
func TestAuthorizeAllowsBearerWithoutFetchSite(t *testing.T) {
	a := newAuth(t)
	r := httptest.NewRequest(http.MethodPost, "/u/nana/statuses", nil)
	r.Header.Set("Authorization", "Bearer s3cret-token")
	if err := a.Authorize(r, true); err != nil {
		t.Errorf("Authorize() = %v, want nil", err)
	}
}

func TestAuthorizeRejectsUnauthenticated(t *testing.T) {
	a := newAuth(t)
	r := httptest.NewRequest(http.MethodPost, "/u/nana/statuses", nil)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	if err := a.Authorize(r, true); err != ErrUnauthenticated {
		t.Errorf("Authorize() = %v, want ErrUnauthenticated", err)
	}
}

func TestClearSessionCookie(t *testing.T) {
	c := newAuth(t).ClearSessionCookie()
	if c.MaxAge >= 0 {
		t.Errorf("MaxAge = %d, want negative", c.MaxAge)
	}
	if c.Value != "" {
		t.Errorf("Value = %q, want empty", c.Value)
	}
}

func TestWantsHTML(t *testing.T) {
	for _, tt := range []struct {
		accept string
		want   bool
	}{
		{"text/html,application/xhtml+xml", true},
		{"application/activity+json", false},
		{"", false},
	} {
		r := httptest.NewRequest(http.MethodGet, "/timeline", nil)
		if tt.accept != "" {
			r.Header.Set("Accept", tt.accept)
		}
		if got := WantsHTML(r); got != tt.want {
			t.Errorf("WantsHTML(%q) = %v, want %v", tt.accept, got, tt.want)
		}
	}
}

func flipLastByte(s string) string {
	if s == "" {
		return "x"
	}
	b := []byte(s)
	if b[len(b)-1] == 'A' {
		b[len(b)-1] = 'B'
	} else {
		b[len(b)-1] = 'A'
	}
	return string(b)
}
