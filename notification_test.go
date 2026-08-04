package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/nna774/s.nna774.net/activitystream"
	"github.com/nna774/s.nna774.net/config"
	"github.com/nna774/s.nna774.net/web"
)

// withTestConfig は Config を差し替える。自分宛かどうかの判定は自分の
// actor URI を見るため、設定が無いと何も判定できない。
func withTestConfig(t *testing.T) string {
	t.Helper()
	saved := Config
	Config = &config.Config{Origin: "https://s.example", Username: "nana"}
	t.Cleanup(func() { Config = saved })
	return Config.ID()
}

func TestIsMyStatus(t *testing.T) {
	me := withTestConfig(t)

	for _, tt := range []struct {
		in   string
		want bool
	}{
		{me + "/status/12", true},
		{me + "/status/0", true},
		{me, false},
		{"", false},
		{"https://pawoo.net/users/kugayama/status/12", false},
		// origin が違う。他インスタンスに同じ形の URI を作られても
		// 自分の投稿と誤認してはならない。
		{"https://evil.example/u/nana/status/12", false},
		// prefix が途中まで一致するだけの別ホスト。
		{"https://s.example.evil/u/nana/status/12", false},
	} {
		if got := isMyStatus(tt.in); got != tt.want {
			t.Errorf("isMyStatus(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

// note は自分宛判定のテスト用に Note を組む。
func note(inReplyTo string, to, cc []string, mentionHref string) *activitystream.Object {
	n := &activitystream.Object{
		ID:      "https://pawoo.net/users/kugayama/statuses/1",
		Type:    activitystream.NoteType,
		Content: "<p>やあ</p>",
		To:      to,
		Cc:      cc,
	}
	if inReplyTo != "" {
		n.InReplyTo = activitystream.URIRef(inReplyTo)
	}
	if mentionHref != "" {
		n.Tag = activitystream.Objects{activitystream.NewMention("@nana@s.example", mentionHref)}
	}
	return n
}

// テンプレートはパースだけでは通らない誤りがある。フィールド名の誤りや
// 未定義の関数呼び出しは描画時に初めて出る。通知ページは種別ごとに文面を
// 分けているので、全種別を1回描画して確かめる。
func TestNotificationsPageRenders(t *testing.T) {
	me := withTestConfig(t)
	const other = "https://pawoo.net/users/someone"

	page := notificationsPage{
		pageBase: pageBase{
			Title: "通知", SiteName: "s", Origin: "https://s.example",
			LocalPart: "nana", Handle: "@nana", Authed: true, UnreadCount: 3,
		},
		Items: []notificationItem{
			{Kind: kindLike, ActorName: "someone", ActorURI: other,
				TargetURI: me + "/status/1", TargetExcerpt: "いいねされた投稿", Unread: true},
			{Kind: kindAnnounce, ActorName: "someone", ActorURI: other,
				TargetURI: me + "/status/2", TargetExcerpt: "ブーストされた投稿"},
			{Kind: kindMention, ActorName: "someone", ActorURI: other,
				ObjectURI: other + "/statuses/9", Content: "<p>やあ</p>",
				TargetExcerpt: "返信された投稿", Unread: true},
			{Kind: kindFollow, ActorName: "someone", ActorURI: other},
			// 取り消しと削除も出来事として並ぶ。元の通知は消さない。
			{Kind: kindUndoLike, ActorName: "someone", ActorURI: other,
				TargetURI: me + "/status/1", TargetExcerpt: "いいねを取り消された投稿"},
			{Kind: kindUndoAnnounce, ActorName: "someone", ActorURI: other,
				TargetURI: me + "/status/2", TargetExcerpt: "ブーストを取り消された投稿"},
			{Kind: kindDelete, ActorName: "someone", ActorURI: other,
				ObjectURI: other + "/statuses/9"},
			// 消した投稿へのいいねが残っていることはある。抜粋が空でも
			// 描画が壊れてはならない。
			{Kind: kindLike, ActorName: "someone", ActorURI: other, TargetURI: me + "/status/3"},
		},
	}

	buf := &bytes.Buffer{}
	if err := web.Render(buf, "notifications", page); err != nil {
		t.Fatalf("rendering the notifications page failed: %v", err)
	}
	got := buf.String()
	for _, want := range []string{
		"がいいねした", "がブーストした", "にフォローされた", "から返信",
		"がいいねを取り消した", "がブーストを取り消した", "が投稿を削除した",
		"いいねされた投稿", "いいねを取り消された投稿", "(この投稿は既に無い)",
		// 削除された投稿は URI だけを取り消し線で出す。
		`class="target gone"`,
		// 未読の印とヘッダのバッジ。
		`class="notice unread"`, `class="badge"`,
		// 返信フォームとフォロー返しボタン（画面遷移せず JS で叩く）。
		// html/template の JS エスケープで "/" は "\/" になる。
		"/timeline?in_reply_to=", `followBack(event, 'https:\/\/pawoo.net\/users\/someone', 'nana')`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered page does not contain %q", want)
		}
	}
}

func TestNotifiesMe(t *testing.T) {
	me := withTestConfig(t)
	const other = "https://pawoo.net/users/someone"
	const public = activitystream.ToPublic

	for _, tt := range []struct {
		name string
		in   *activitystream.Object
		note *activitystream.Object
		want bool
	}{
		{
			name: "自分の投稿への返信",
			in:   &activitystream.Object{Type: activitystream.CreateType},
			note: note(me+"/status/3", []string{public}, nil, ""),
			want: true,
		},
		{
			name: "cc に自分",
			in:   &activitystream.Object{Type: activitystream.CreateType},
			note: note("", []string{public}, []string{me}, ""),
			want: true,
		},
		{
			name: "to に自分",
			in:   &activitystream.Object{Type: activitystream.CreateType},
			note: note("", []string{me}, nil, ""),
			want: true,
		},
		{
			// 宛先が Activity 側にしか書かれない実装がある。
			name: "Activity の cc に自分",
			in:   &activitystream.Object{Type: activitystream.CreateType, Cc: []string{me}},
			note: note("", []string{public}, nil, ""),
			want: true,
		},
		{
			name: "Mention タグが自分を指す",
			in:   &activitystream.Object{Type: activitystream.CreateType},
			note: note("", []string{public}, nil, me),
			want: true,
		},
		{
			// フォロー相手同士の会話。タイムラインには流すが通知にはしない。
			name: "他人宛の返信",
			in:   &activitystream.Object{Type: activitystream.CreateType},
			note: note(other+"/statuses/9", []string{public}, []string{other}, other),
			want: false,
		},
		{
			name: "ただの公開投稿",
			in:   &activitystream.Object{Type: activitystream.CreateType},
			note: note("", []string{public}, nil, ""),
			want: false,
		},
		{
			// Public 宛や followers 宛を自分宛と誤認しないこと。誤認すると
			// フォロー相手の投稿すべてが通知になる。
			name: "followers 宛",
			in:   &activitystream.Object{Type: activitystream.CreateType},
			note: note("", []string{other + "/followers"}, []string{public}, ""),
			want: false,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := notifiesMe(tt.in, tt.note); got != tt.want {
				t.Errorf("notifiesMe() = %v, want %v", got, tt.want)
			}
		})
	}
}
