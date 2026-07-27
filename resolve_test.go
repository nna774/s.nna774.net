package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResolveActorURIAcceptsURLsDirectly(t *testing.T) {
	for _, in := range []string{
		"https://pawoo.net/users/kugayama",
		"http://example.com/u/x",
		"  https://pawoo.net/users/kugayama  ",
	} {
		got, err := resolveActorURI(context.Background(), in)
		if err != nil {
			t.Errorf("resolveActorURI(%q): %v", in, err)
			continue
		}
		if got != strings.TrimSpace(in) {
			t.Errorf("resolveActorURI(%q) = %q", in, got)
		}
	}
}

func TestResolveActorURIRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "   ", "nothandle", "@", "@@", "user@", "@host"} {
		if _, err := resolveActorURI(context.Background(), in); err == nil {
			t.Errorf("resolveActorURI(%q) succeeded, want error", in)
		}
	}
}

// webfinger の JRD から rel=self かつ activity+json の href を取ること。
// profile-page の href を拾ってしまうと actor ではなく HTML を fetch して
// しまう。
func TestWebfingerLookupPicksTheActivityJSONSelfLink(t *testing.T) {
	var gotResource string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotResource = r.URL.Query().Get("resource")
		w.Header().Set("Content-Type", "application/jrd+json")
		_, _ = w.Write([]byte(`{
		  "subject": "acct:x@example.com",
		  "links": [
		    {"rel":"http://webfinger.net/rel/profile-page","type":"text/html","href":"https://example.com/@x"},
		    {"rel":"self","type":"application/activity+json","href":"https://example.com/users/x"},
		    {"rel":"http://ostatus.org/schema/1.0/subscribe","template":"https://example.com/i?uri={uri}"}
		  ]
		}`))
	}))
	defer srv.Close()

	// httptest の TLS サーバは自己署名なので、そのクライアントを使う。
	old := http.DefaultClient
	http.DefaultClient = srv.Client()
	defer func() { http.DefaultClient = old }()

	host := strings.TrimPrefix(srv.URL, "https://")
	// webfingerLookup は https を組み立てるので、host にポートを含めて渡す。
	got, err := webfingerLookup(context.Background(), "x", host)
	if err != nil {
		t.Fatalf("webfingerLookup: %v", err)
	}
	if want := "https://example.com/users/x"; got != want {
		t.Errorf("webfingerLookup = %q, want %q", got, want)
	}
	if want := "acct:x@" + host; gotResource != want {
		t.Errorf("resource = %q, want %q", gotResource, want)
	}
}

func TestWebfingerLookupFailsWithoutActorLink(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"subject":"acct:x@example.com","links":[
		  {"rel":"http://webfinger.net/rel/profile-page","type":"text/html","href":"https://example.com/@x"}]}`))
	}))
	defer srv.Close()

	old := http.DefaultClient
	http.DefaultClient = srv.Client()
	defer func() { http.DefaultClient = old }()

	if _, err := webfingerLookup(context.Background(), "x", strings.TrimPrefix(srv.URL, "https://")); err == nil {
		t.Error("webfingerLookup succeeded without an actor link")
	}
}

func TestIsSafeRedirect(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want bool
	}{
		{"/timeline", true},
		{"/u/nana", true},
		{"", false},
		{"//evil.example", false},
		{"https://evil.example", false},
		{"timeline", false},
	} {
		if got := isSafeRedirect(tt.in); got != tt.want {
			t.Errorf("isSafeRedirect(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestSameOrigin(t *testing.T) {
	for _, tt := range []struct {
		a, b string
		want bool
	}{
		{"https://pawoo.net/users/x#main-key", "https://pawoo.net/users/x", true},
		{"https://pawoo.net/users/x", "https://pawoo.net/users/y", true},
		{"https://pawoo.net/users/x", "https://evil.example/users/x", false},
		{"http://pawoo.net/users/x", "https://pawoo.net/users/x", false},
		{"not a uri", "https://pawoo.net/users/x", false},
	} {
		if got := sameOrigin(tt.a, tt.b); got != tt.want {
			t.Errorf("sameOrigin(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestActorURIFromKeyID(t *testing.T) {
	for _, tt := range []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"https://pawoo.net/users/x#main-key", "https://pawoo.net/users/x", false},
		{"https://pawoo.net/users/x", "https://pawoo.net/users/x", false},
		{"ftp://pawoo.net/x", "", true},
		{"not a uri at all", "", true},
	} {
		got, err := actorURIFromKeyID(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("actorURIFromKeyID(%q) succeeded, want error", tt.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("actorURIFromKeyID(%q): %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("actorURIFromKeyID(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestStripTagsAndExcerpt(t *testing.T) {
	got := excerpt(`<p>あいうえお</p><a href="x">かきくけこ</a>`, 7)
	if want := "あいうえおかき…"; got != want {
		t.Errorf("excerpt = %q, want %q", got, want)
	}
	if got := excerpt("<p>短い</p>", 40); got != "短い" {
		t.Errorf("excerpt = %q, want %q", got, "短い")
	}
}
