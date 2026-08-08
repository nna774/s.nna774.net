// Package web は HTML の描画と、リモート由来の HTML のサニタイズを担う。
package web

import (
	"embed"
	"fmt"
	"html/template"
	"io"
	"regexp"
	"time"

	"github.com/microcosm-cc/bluemonday"
)

//go:embed templates/*.html
var templateFS embed.FS

// ページごとに独立したテンプレートセットを作る。各ページが自分の
// "content" を定義するため、1つのセットに全部入れると名前が衝突する。
var pages = func() map[string]*template.Template {
	names := []string{"profile", "status", "statuses", "timeline", "notifications", "login", "collection", "remote", "favorites", "announce", "status_likes", "status_announces"}
	m := make(map[string]*template.Template, len(names))
	for _, name := range names {
		m[name] = template.Must(template.New(name).Funcs(funcs).
			ParseFS(templateFS, "templates/layout.html", "templates/"+name+".html"))
	}
	return m
}()

// policy はリモートの投稿本文に適用するサニタイズ規則。
//
// 他インスタンス由来の content は信用できない HTML である。自分で
// 許可リストを書くと必ず抜けが出るため、この用途向けに作られた
// bluemonday を使う。
var policy = func() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.AllowElements("p", "br", "span", "em", "strong", "b", "i", "code", "pre", "blockquote", "ul", "ol", "li")
	// Mastodon のメンションやハッシュタグのリンクを残す。
	p.AllowAttrs("href").OnElements("a")
	p.AllowAttrs("class").OnElements("a", "span")
	// href は http/https と mailto だけに限る。javascript: や data: を通さない。
	p.AllowURLSchemes("http", "https", "mailto")
	// 外部リンクに rel を付ける。
	p.RequireNoFollowOnLinks(true)
	p.AddTargetBlankToFullyQualifiedLinks(true)
	return p
}()

// Sanitize はリモート由来の HTML を安全な断片に落とす。
func Sanitize(html string) template.HTML {
	return template.HTML(policy.Sanitize(html))
}

var funcs = template.FuncMap{
	"sanitize": Sanitize,
	"datetime": humanTime,
	"acct":     acct,
}

// humanTime は ActivityStreams の published を読みやすくする。実装に
// よって ISO8601 と RFC1123 のどちらも来るため両方受ける。
func humanTime(s string) string {
	for _, layout := range []string{time.RFC3339, time.RFC1123, time.RFC1123Z, time.RFC3339Nano} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Local().Format("2006-01-02 15:04")
		}
	}
	return s
}

var actorURIPattern = regexp.MustCompile(`^https?://([^/]+)/(?:users/|u/|@)?([^/]+)/?$`)

// acct は actor の URI を @user@host 風の表記に直す。表示用の目安であり、
// 厳密な解決ではない。
func acct(uri string) string {
	m := actorURIPattern.FindStringSubmatch(uri)
	if m == nil {
		return uri
	}
	return fmt.Sprintf("@%s@%s", m[2], m[1])
}

// Render は名前付きのページを描画する。
func Render(w io.Writer, page string, data interface{}) error {
	t, ok := pages[page]
	if !ok {
		return fmt.Errorf("web: unknown page %q", page)
	}
	return t.ExecuteTemplate(w, "layout", data)
}
