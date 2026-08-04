package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/nna774/s.nna774.net/web"
)

func TestParseReactionRequestFromForm(t *testing.T) {
	form := url.Values{"object": {"https://example.com/status/1"}, "actor": {"https://example.com/users/x"}}
	r := httptest.NewRequest(http.MethodPost, "/u/nana/likes", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	object, actor, herr := parseReactionRequest(r)
	if herr != nil {
		t.Fatalf("parseReactionRequest: %v", herr)
	}
	if object != "https://example.com/status/1" || actor != "https://example.com/users/x" {
		t.Errorf("parseReactionRequest = (%q, %q)", object, actor)
	}
}

func TestParseReactionRequestFromJSON(t *testing.T) {
	body := `{"object":"https://example.com/status/1","actor":"https://example.com/users/x"}`
	r := httptest.NewRequest(http.MethodPost, "/u/nana/likes", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	object, actor, herr := parseReactionRequest(r)
	if herr != nil {
		t.Fatalf("parseReactionRequest: %v", herr)
	}
	if object != "https://example.com/status/1" || actor != "https://example.com/users/x" {
		t.Errorf("parseReactionRequest = (%q, %q)", object, actor)
	}
}

func TestParseReactionRequestRejectsMissingFields(t *testing.T) {
	for _, body := range []string{
		`{"object":"","actor":"https://example.com/users/x"}`,
		`{"object":"https://example.com/status/1","actor":""}`,
	} {
		r := httptest.NewRequest(http.MethodPost, "/u/nana/likes", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if _, _, herr := parseReactionRequest(r); herr == nil {
			t.Errorf("parseReactionRequest(%v) succeeded, want error", body)
		}
	}
}

func TestObjectFromQuery(t *testing.T) {
	r := httptest.NewRequest(http.MethodDelete, "/u/nana/likes?object=https%3A%2F%2Fexample.com%2Fstatus%2F1", nil)
	object, herr := objectFromQuery(r)
	if herr != nil {
		t.Fatalf("objectFromQuery: %v", herr)
	}
	if want := "https://example.com/status/1"; object != want {
		t.Errorf("objectFromQuery = %q, want %q", object, want)
	}

	r = httptest.NewRequest(http.MethodDelete, "/u/nana/likes", nil)
	if _, herr := objectFromQuery(r); herr == nil {
		t.Error("objectFromQuery succeeded without an object, want error")
	}
}

// like / boost / unlike / unboost / favorites のルートが登録されている
// ことを確かめる。
func TestReactionRoutes(t *testing.T) {
	r := newRouter()
	cases := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/u/nana/likes"},
		{http.MethodDelete, "/u/nana/likes"},
		{http.MethodPost, "/u/nana/boosts"},
		{http.MethodDelete, "/u/nana/boosts"},
		{http.MethodGet, "/u/nana/favorites"},
	}
	for _, c := range cases {
		h, _, _ := r.Lookup(c.method, c.path)
		if h == nil {
			t.Errorf("%v %v has no handler", c.method, c.path)
		}
	}
}

// タイムラインの各投稿にいいね・ブーストのボタンが状態に応じて出し分けられる
// ことを確かめる。
func TestTimelinePageRendersReactionButtons(t *testing.T) {
	page := timelinePage{
		pageBase: pageBase{Title: "タイムライン", SiteName: "nana", LocalPart: "nana", Handle: "@nana"},
		Items: []timelineItem{
			{
				AuthorName: "someone",
				AuthorURI:  "https://example.com/users/someone",
				ObjectURI:  "https://example.com/status/1",
				Content:    "<p>未反応</p>",
				Published:  "2026-08-03T12:00:00Z",
			},
			{
				AuthorName: "other",
				AuthorURI:  "https://example.com/users/other",
				ObjectURI:  "https://example.com/status/2",
				Content:    "<p>反応済み</p>",
				Published:  "2026-08-03T12:00:00Z",
				Liked:      true,
				Boosted:    true,
			},
		},
	}
	buf := &bytes.Buffer{}
	if err := web.Render(buf, "timeline", page); err != nil {
		t.Fatalf("rendering timeline failed: %v", err)
	}
	html := buf.String()
	// html/template は JS 文字列リテラルの中で "/" を "\/" にエスケープする。
	for _, want := range []string{
		`likeStatus(event, 'https:\/\/example.com\/status\/1', 'https:\/\/example.com\/users\/someone', 'nana')`,
		`boostStatus(event, 'https:\/\/example.com\/status\/1', 'https:\/\/example.com\/users\/someone', 'nana')`,
		`unlikeStatus(event, 'https:\/\/example.com\/status\/2', 'nana')`,
		`unboostStatus(event, 'https:\/\/example.com\/status\/2', 'nana')`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered page does not contain %q", want)
		}
	}
}

func TestFavoritesPageRenders(t *testing.T) {
	page := favoritesPage{
		pageBase: pageBase{Title: "いいね", SiteName: "nana", LocalPart: "nana", Handle: "@nana"},
		Items: []favoriteItem{{
			ObjectURI:  "https://example.com/status/1",
			AuthorName: "someone",
			AuthorURI:  "https://example.com/users/someone",
			At:         "2026-08-03T12:00:00Z",
		}},
	}
	buf := &bytes.Buffer{}
	if err := web.Render(buf, "favorites", page); err != nil {
		t.Fatalf("rendering favorites failed: %v", err)
	}
	html := buf.String()
	for _, want := range []string{
		`href="https://example.com/status/1"`,
		`href="/remote?actor=https%3a%2f%2fexample.com%2fusers%2fsomeone"`,
		"someone",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered page does not contain %q", want)
		}
	}
}

func TestFavoritesPageRendersEmpty(t *testing.T) {
	page := favoritesPage{pageBase: pageBase{Title: "いいね", SiteName: "nana", LocalPart: "nana", Handle: "@nana"}}
	buf := &bytes.Buffer{}
	if err := web.Render(buf, "favorites", page); err != nil {
		t.Fatalf("rendering favorites failed: %v", err)
	}
	if !strings.Contains(buf.String(), "まだいいねしたものは無い") {
		t.Error("rendered page does not show the empty state")
	}
}
