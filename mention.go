package main

import (
	"context"
	"fmt"
	"html"
	"regexp"
	"strings"

	"github.com/nna774/s.nna774.net/activitystream"
)

// mentionPattern は本文中の @user@host を拾う。
//
// Mastodon は本文に書いた @user@host を自動でメンションとして認識する。
// こちらも同じにしないと、書いたつもりのメンションが tag にも cc にも
// 入らず、相手に何も届かない。
var mentionPattern = regexp.MustCompile(`@([A-Za-z0-9_]+(?:[.\-][A-Za-z0-9_]+)*)@([A-Za-z0-9]([A-Za-z0-9\-]*[A-Za-z0-9])?(?:\.[A-Za-z0-9]([A-Za-z0-9\-]*[A-Za-z0-9])?)+)`)

// urlPattern は本文中の http(s) URL を拾う。RFC3986 の url-safe な文字集合
// のうち代表的なものだけを許可する。ぷにこーど等の対応はしない。
var urlPattern = regexp.MustCompile(`https?://[-A-Za-z0-9._~:/?#\[\]@!$&'()*+,;=%]+`)

// urlTrailingCutset は URL の末尾から切り離す約物。文末にそのまま URL を
// 書くと句読点や閉じ括弧まで URL に含めて拾ってしまうため。コロンと
// セミコロンは含めない。ポート番号や、エスケープで生まれた "&amp;" の
// ";" まで削ってしまうため。
const urlTrailingCutset = ".,!?)]}、。」』"

type mention struct {
	// Handle は @user@host の表記。
	Handle string
	// ActorURI は解決した actor の URI。
	ActorURI string
}

// collectMentions は本文中の @user@host と、明示的に渡された宛先を
// あわせて actor URI に解決する。解決できなかったものは黙って捨てる。
// 1つ引けないだけで投稿自体を失敗させる意味は無い。
func collectMentions(ctx context.Context, content string, explicit []string) []mention {
	seen := map[string]bool{}
	out := make([]mention, 0)

	add := func(handle, uri string) {
		if uri == "" || seen[uri] {
			return
		}
		seen[uri] = true
		out = append(out, mention{Handle: handle, ActorURI: uri})
	}

	for _, m := range mentionPattern.FindAllStringSubmatch(content, -1) {
		handle := m[0]
		uri, err := webfingerLookup(ctx, m[1], m[2])
		if err != nil {
			logf("cannot resolve mention %v: %v", handle, err)
			continue
		}
		add(handle, uri)
	}

	for _, e := range explicit {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		uri, err := resolveActorURI(ctx, e)
		if err != nil {
			logf("cannot resolve mention %v: %v", e, err)
			continue
		}
		add(handleFor(ctx, e, uri), uri)
	}
	return out
}

// handleFor は表示に使う @user@host を決める。入力がハンドルならそれを
// 使い、URI なら actor から組む。
func handleFor(ctx context.Context, input, actorURI string) string {
	if strings.Contains(input, "@") && !strings.HasPrefix(input, "http") {
		s := strings.TrimPrefix(strings.TrimPrefix(input, "acct:"), "@")
		return "@" + s
	}
	return mentionName(ctx, actorURI)
}

// renderContent は入力の平文を ActivityStreams の content にする。
//
// content は HTML である。平文をそのまま入れると改行が失われ、"<" などが
// 相手側で壊れる。エスケープしてから URL・メンションをリンクにし、段落に
// 組む。順序を逆にすると、エスケープでリンクの山括弧まで潰れる。
//
// URL のリンク化はメンションより先に行う。逆にすると、メンションリンクの
// href に入った actor URI（httpから始まる）をもう一度 URL として拾って
// 二重にリンクを差し込んでしまう。
func renderContent(text string, mentions []mention) string {
	escaped := html.EscapeString(strings.ReplaceAll(text, "\r\n", "\n"))
	escaped = linkifyURLs(escaped)

	// 正規表現で1回だけ走査して置換する。ハンドルごとに
	// strings.ReplaceAll を繰り返すと、挿入したリンクの中に含まれる
	// 短いハンドルに次の置換が当たってリンクが入れ子になる
	// (@a@example.com が @a@example.com.br の内側に当たる)。
	byHandle := make(map[string]string, len(mentions))
	for _, m := range mentions {
		if m.Handle != "" {
			byHandle[m.Handle] = m.ActorURI
		}
	}
	escaped = mentionPattern.ReplaceAllStringFunc(escaped, func(handle string) string {
		uri, ok := byHandle[handle]
		if !ok {
			// 解決できなかったものは平文のまま残す。
			return handle
		}
		return fmt.Sprintf(`<a href="%s" class="u-url mention">%s</a>`,
			html.EscapeString(uri), html.EscapeString(handle))
	})

	paragraphs := strings.Split(escaped, "\n\n")
	var b strings.Builder
	for _, p := range paragraphs {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		b.WriteString("<p>")
		b.WriteString(strings.ReplaceAll(p, "\n", "<br>"))
		b.WriteString("</p>")
	}
	if b.Len() == 0 {
		return "<p></p>"
	}
	return b.String()
}

// linkifyURLs は本文中の URL をリンクにする。エスケープ済みの文字列に
// 対して呼ぶこと。エスケープ前だと "&" が3文字（&amp;）に膨らんだ後の
// 位置がずれる。
func linkifyURLs(escaped string) string {
	return urlPattern.ReplaceAllStringFunc(escaped, func(u string) string {
		trimmed := strings.TrimRight(u, urlTrailingCutset)
		if trimmed == "" {
			return u
		}
		rest := u[len(trimmed):]
		return fmt.Sprintf(`<a href="%s">%s</a>`, trimmed, trimmed) + rest
	})
}

// mentionTags は content に載せる Mention タグを組む。
func mentionTags(mentions []mention) []*activitystream.Object {
	tags := make([]*activitystream.Object, 0, len(mentions))
	for _, m := range mentions {
		tags = append(tags, activitystream.NewMention(m.Handle, m.ActorURI))
	}
	return tags
}

func mentionURIs(mentions []mention) []string {
	uris := make([]string, 0, len(mentions))
	for _, m := range mentions {
		uris = append(uris, m.ActorURI)
	}
	return uris
}
