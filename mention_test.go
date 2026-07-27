package main

import (
	"strings"
	"testing"
)

// 本文中の @user@host を拾えること。Mastodon は自動で認識するので、
// これができないと書いたつもりのメンションが誰にも届かない。
func TestMentionPatternFindsHandlesInText(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   string
		want []string
	}{
		{"単独", "@kugayama@pawoo.net", []string{"@kugayama@pawoo.net"}},
		{"文中", "やあ @kugayama@pawoo.net 元気か", []string{"@kugayama@pawoo.net"}},
		{"複数", "@a@example.com と @b@sub.example.org", []string{"@a@example.com", "@b@sub.example.org"}},
		{"行頭行末", "@a@example.com\n本文\n@b@example.com", []string{"@a@example.com", "@b@example.com"}},
		{"アンダースコアとハイフン", "@my_user-name@ex-ample.co.jp", []string{"@my_user-name@ex-ample.co.jp"}},
		{"ローカルのみは拾わない", "@kugayama だけ", nil},
		{"メールらしきものは拾わない", "nana@example.com", nil},
		{"TLDなしは拾わない", "@a@localhost", nil},
		{"なし", "ふつうの本文", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ms := mentionPattern.FindAllString(tt.in, -1)
			if len(ms) != len(tt.want) {
				t.Fatalf("got %v, want %v", ms, tt.want)
			}
			for i := range tt.want {
				if ms[i] != tt.want[i] {
					t.Errorf("[%d] = %q, want %q", i, ms[i], tt.want[i])
				}
			}
		})
	}
}

// content は HTML である。平文をそのまま入れると改行が失われ、"<" が
// 相手側で壊れる。
func TestRenderContentEscapesAndParagraphs(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   string
		want string
	}{
		{"1行", "やっぴー", "<p>やっぴー</p>"},
		{"改行は br", "あ\nい", "<p>あ<br>い</p>"},
		{"空行は段落", "あ\n\nい", "<p>あ</p><p>い</p>"},
		{"CRLF", "あ\r\nい", "<p>あ<br>い</p>"},
		{"HTMLをエスケープ", "<script>alert(1)</script>", "<p>&lt;script&gt;alert(1)&lt;/script&gt;</p>"},
		{"アンパサンド", "a & b", "<p>a &amp; b</p>"},
		{"空", "", "<p></p>"},
		{"空白だけ", "   \n  ", "<p></p>"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := renderContent(tt.in, nil); got != tt.want {
				t.Errorf("renderContent(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestRenderContentLinkifiesMentions(t *testing.T) {
	ms := []mention{{Handle: "@kugayama@pawoo.net", ActorURI: "https://pawoo.net/users/kugayama"}}
	got := renderContent("やあ @kugayama@pawoo.net", ms)

	want := `<p>やあ <a href="https://pawoo.net/users/kugayama" class="u-url mention">@kugayama@pawoo.net</a></p>`
	if got != want {
		t.Errorf("renderContent = %q\nwant %q", got, want)
	}
}

// 短いハンドルを先に置換すると、長い方のリンクが途中で切れる。
func TestRenderContentHandlesOverlappingHandles(t *testing.T) {
	ms := []mention{
		{Handle: "@a@example.com", ActorURI: "https://example.com/u/a"},
		{Handle: "@a@example.com.br", ActorURI: "https://example.com.br/u/a"},
	}
	got := renderContent("@a@example.com.br と @a@example.com", ms)

	// 長い方が壊れていないこと。
	if !strings.Contains(got, `<a href="https://example.com.br/u/a" class="u-url mention">@a@example.com.br</a>`) {
		t.Errorf("the longer handle was mangled: %s", got)
	}
	// リンクが入れ子になっていないこと。
	if strings.Contains(got, "<a href=\"https://example.com/u/a\" class=\"u-url mention\">@a@example.com</a>.br") {
		t.Errorf("the shorter handle matched inside the longer one: %s", got)
	}
}

// メンション先の URI はリンクにするときエスケープされること。
func TestRenderContentEscapesMentionURI(t *testing.T) {
	ms := []mention{{Handle: "@x@e.com", ActorURI: `https://e.com/u/x"><script>`}}
	got := renderContent("@x@e.com", ms)
	if strings.Contains(got, "<script>") {
		t.Errorf("the actor URI was not escaped: %s", got)
	}
}

func TestMentionTagsAndURIs(t *testing.T) {
	ms := []mention{
		{Handle: "@a@x.com", ActorURI: "https://x.com/u/a"},
		{Handle: "@b@y.com", ActorURI: "https://y.com/u/b"},
	}
	tags := mentionTags(ms)
	if len(tags) != 2 {
		t.Fatalf("len(tags) = %d, want 2", len(tags))
	}
	for i, tag := range tags {
		if tag.Type != "Mention" {
			t.Errorf("tags[%d].Type = %q, want Mention", i, tag.Type)
		}
		if tag.Name != ms[i].Handle {
			t.Errorf("tags[%d].Name = %q, want %q", i, tag.Name, ms[i].Handle)
		}
		if tag.Href != ms[i].ActorURI {
			t.Errorf("tags[%d].Href = %q, want %q", i, tag.Href, ms[i].ActorURI)
		}
	}
	uris := mentionURIs(ms)
	if len(uris) != 2 || uris[0] != "https://x.com/u/a" || uris[1] != "https://y.com/u/b" {
		t.Errorf("mentionURIs = %v", uris)
	}
}

// 公開範囲ごとに to / cc がどう決まるか。メンション先は cc に入って
// いなければ相手に届かない。
func TestAudience(t *testing.T) {
	const followers = "https://s.nna774.net/u/nana/followers"
	const pub = "https://www.w3.org/ns/activitystreams#Public"
	mentioned := []string{"https://pawoo.net/users/kugayama"}

	for _, tt := range []struct {
		visibility string
		wantTo     []string
		wantCc     []string
	}{
		{visibilityPublic, []string{pub}, []string{followers, mentioned[0]}},
		{visibilityUnlisted, []string{followers}, []string{pub, mentioned[0]}},
		{visibilityFollowers, []string{followers}, []string{mentioned[0]}},
	} {
		t.Run(tt.visibility, func(t *testing.T) {
			req := &statusRequest{Content: "x", Visibility: tt.visibility}
			to, cc := req.audience(followers, mentioned)
			assertSlice(t, "to", to, tt.wantTo)
			assertSlice(t, "cc", cc, tt.wantCc)
		})
	}
}

func TestStatusRequestNormalize(t *testing.T) {
	t.Run("空の本文は拒否", func(t *testing.T) {
		for _, c := range []string{"", "   ", "\n\n"} {
			req := &statusRequest{Content: c}
			if err := req.normalize(); err == nil {
				t.Errorf("normalize accepted %q", c)
			}
		}
	})
	t.Run("省略時は public", func(t *testing.T) {
		req := &statusRequest{Content: "x"}
		if err := req.normalize(); err != nil {
			t.Fatalf("normalize: %v", err)
		}
		if req.Visibility != visibilityPublic {
			t.Errorf("Visibility = %q, want %q", req.Visibility, visibilityPublic)
		}
	})
	t.Run("未知の公開範囲は拒否", func(t *testing.T) {
		req := &statusRequest{Content: "x", Visibility: "secret"}
		if err := req.normalize(); err == nil {
			t.Error("normalize accepted an unknown visibility")
		}
	})
}

func assertSlice(t *testing.T, name string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s[%d] = %q, want %q", name, i, got[i], want[i])
		}
	}
}
