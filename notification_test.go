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
	primary := &config.ActorConfig{Username: "nana", Primary: true, ActorType: config.ActorTypePerson, Origin: "https://s.example"}
	bot := &config.ActorConfig{Username: "bot", Name: "bot", ActorType: config.ActorTypeService, Origin: "https://s.example"}
	Config = &config.Config{Origin: "https://s.example", Actors: []*config.ActorConfig{primary, bot}}
	t.Cleanup(func() { Config = saved })
	return primary.ID()
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
			{Kind: kindFollow, ActorName: "someone", ActorURI: other, RecipientCanFollowBack: true},
			// bot (sub actor) 宛のフォローは宛先ラベルを localpart で出し、
			// following を持たない bot では「フォローを返す」ボタンを出さ
			// ない。sub actor は複数あり得るので表示名ではなく localpart で
			// 一意に示す。
			{Kind: kindFollow, ActorName: "someone else", ActorURI: other,
				RecipientLocalPart: "bot"},
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
		"がいいねした", "がRTした", "にフォローされた", "から返信",
		"がいいねを取り消した", "がRTを取り消した", "が投稿を削除した",
		"いいねされた投稿", "いいねを取り消された投稿", "(この投稿は既に無い)",
		// 削除された投稿は URI だけを取り消し線で出す。
		`class="target gone"`,
		// 未読の印とヘッダのバッジ。
		`class="notice unread"`, `class="badge"`,
		// 返信フォームとフォロー返しボタン（画面遷移せず JS で叩く）。
		// html/template の JS エスケープで "/" は "\/" になる。
		"/timeline?in_reply_to=", `followBack(event, 'https:\/\/pawoo.net\/users\/someone', 'nana')`,
		// bot 宛の通知には localpart そのままの宛先ラベルが出る。
		"@bot 宛",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered page does not contain %q", want)
		}
	}

	// bot 宛のフォロー通知にはフォロー返しボタンを出さない (following は
	// primary actor 専用の機能で、nana として押すと別人をフォロー返す
	// 誤動作になるため)。primary 宛のフォロー通知にはボタンが出るので、
	// ボタンの出現回数がちょうど primary 宛の1件分であることを見る。
	if n := strings.Count(got, "followBack(event"); n != 1 {
		t.Errorf("followBack button appears %d times, want 1 (only for the primary-recipient follow)", n)
	}
}

func TestNotifiesMe(t *testing.T) {
	me := withTestConfig(t)
	bot, _ := Config.ActorByLocalPart("bot")
	const other = "https://pawoo.net/users/someone"
	const public = activitystream.ToPublic

	for _, tt := range []struct {
		name  string
		actor *config.ActorConfig
		in    *activitystream.Object
		note  *activitystream.Object
		want  bool
	}{
		{
			name:  "自分の投稿への返信",
			actor: Config.PrimaryActor(),
			in:    &activitystream.Object{Type: activitystream.CreateType},
			note:  note(me+"/status/3", []string{public}, nil, ""),
			want:  true,
		},
		{
			name:  "cc に自分",
			actor: Config.PrimaryActor(),
			in:    &activitystream.Object{Type: activitystream.CreateType},
			note:  note("", []string{public}, []string{me}, ""),
			want:  true,
		},
		{
			name:  "to に自分",
			actor: Config.PrimaryActor(),
			in:    &activitystream.Object{Type: activitystream.CreateType},
			note:  note("", []string{me}, nil, ""),
			want:  true,
		},
		{
			// 宛先が Activity 側にしか書かれない実装がある。
			name:  "Activity の cc に自分",
			actor: Config.PrimaryActor(),
			in:    &activitystream.Object{Type: activitystream.CreateType, Cc: []string{me}},
			note:  note("", []string{public}, nil, ""),
			want:  true,
		},
		{
			name:  "Mention タグが自分を指す",
			actor: Config.PrimaryActor(),
			in:    &activitystream.Object{Type: activitystream.CreateType},
			note:  note("", []string{public}, nil, me),
			want:  true,
		},
		{
			// フォロー相手同士の会話。タイムラインには流すが通知にはしない。
			name:  "他人宛の返信",
			actor: Config.PrimaryActor(),
			in:    &activitystream.Object{Type: activitystream.CreateType},
			note:  note(other+"/statuses/9", []string{public}, []string{other}, other),
			want:  false,
		},
		{
			name:  "ただの公開投稿",
			actor: Config.PrimaryActor(),
			in:    &activitystream.Object{Type: activitystream.CreateType},
			note:  note("", []string{public}, nil, ""),
			want:  false,
		},
		{
			// Public 宛や followers 宛を自分宛と誤認しないこと。誤認すると
			// フォロー相手の投稿すべてが通知になる。
			name:  "followers 宛",
			actor: Config.PrimaryActor(),
			in:    &activitystream.Object{Type: activitystream.CreateType},
			note:  note("", []string{other + "/followers"}, []string{public}, ""),
			want:  false,
		},
		{
			// bot 自身の投稿への返信は bot 宛。
			name:  "bot の投稿への返信 (bot として判定)",
			actor: bot,
			in:    &activitystream.Object{Type: activitystream.CreateType},
			note:  note(bot.ID()+"/status/1", []string{public}, nil, ""),
			want:  true,
		},
		{
			// nana の投稿への返信を bot の inbox に (取り違えなどで) 送って
			// きても、bot 宛と誤認識してはならない。別の actor の投稿は
			// 「ローカルのどれかの投稿」ではあっても「この actor 自身の
			// 投稿」ではない。
			name:  "nana の投稿への返信を bot として判定 (誤認しないこと)",
			actor: bot,
			in:    &activitystream.Object{Type: activitystream.CreateType},
			note:  note(me+"/status/3", []string{public}, nil, ""),
			want:  false,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := notifiesMe(tt.actor, tt.in, tt.note); got != tt.want {
				t.Errorf("notifiesMe() = %v, want %v", got, tt.want)
			}
		})
	}
}
