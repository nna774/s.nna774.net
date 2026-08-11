package main

import (
	"context"
	"testing"

	"github.com/nna774/s.nna774.net/activitystream"
)

func TestVerifyBoostedNote(t *testing.T) {
	const uri = "https://pawoo.net/users/x/statuses/1"

	// 正しく引けた投稿。各ケースはこれを1箇所だけ崩す。
	valid := func() *activitystream.Object {
		return &activitystream.Object{
			ID:           uri,
			Type:         activitystream.NoteType,
			Content:      "<p>やあ</p>",
			AttributedTo: activitystream.URIRef("https://pawoo.net/users/x"),
		}
	}

	if err := verifyFetchedNote(valid(), uri); err != nil {
		t.Fatalf("正しい投稿が弾かれた: %v", err)
	}

	for _, tt := range []struct {
		name    string
		corrupt func(*activitystream.Object)
	}{
		{
			// 別の投稿を返された。これを通すと、ブーストを装って任意の投稿を
			// タイムラインに並べられる。
			name:    "id が要求した URI と違う",
			corrupt: func(o *activitystream.Object) { o.ID = "https://pawoo.net/users/x/statuses/999" },
		},
		{
			name:    "id が無い",
			corrupt: func(o *activitystream.Object) { o.ID = "" },
		},
		{
			name:    "本文が無い",
			corrupt: func(o *activitystream.Object) { o.Content = "" },
		},
		{
			name:    "attributedTo が無い",
			corrupt: func(o *activitystream.Object) { o.AttributedTo = nil },
		},
		{
			// 著者が別オリジン。これを通すと、あるサーバが「他のサーバの誰かが
			// 書いた」と称する投稿を流し込める。
			name: "著者のオリジンが投稿と違う",
			corrupt: func(o *activitystream.Object) {
				o.AttributedTo = activitystream.URIRef("https://evil.example/users/y")
			},
		},
		{
			name: "著者が scheme 違いの同一ホスト",
			corrupt: func(o *activitystream.Object) {
				o.AttributedTo = activitystream.URIRef("http://pawoo.net/users/x")
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			note := valid()
			tt.corrupt(note)
			if err := verifyFetchedNote(note, uri); err == nil {
				t.Error("弾かれなかった")
			}
		})
	}
}

// replyTargetAuthor はローカルの投稿への返信ならリモートに引きに行かず URI
// だけで著者を判定できる。ネットワークを叩かない分岐だけをここで確かめる。
func TestReplyTargetAuthorLocal(t *testing.T) {
	me := withTestConfig(t)
	bot, _ := Config.ActorByLocalPart("bot")
	ctx := context.Background()

	for _, tt := range []struct {
		name     string
		replyURI string
		want     string
	}{
		{name: "自分の投稿への返信", replyURI: me + "/status/3", want: me},
		{name: "bot の投稿への返信", replyURI: bot.ID() + "/status/1", want: bot.ID()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := replyTargetAuthor(ctx, Config.PrimaryActor(), tt.replyURI, "https://pawoo.net/users/x/statuses/1"); got != tt.want {
				t.Errorf("replyTargetAuthor() = %v, want %v", got, tt.want)
			}
		})
	}
}

// shouldFilterReply は「返信ではない」「スレッドの続き (返信先が投稿者
// 自身)」のときは isFollowing まで進まずに弾かないと判定する。フォロー
// 確認まで進む分岐は datastore と fetchVerifiedNote の実ネットワーク取得が
// 絡むため、ここでは短絡する分岐だけを確かめる (target を意図的にローカル
// かつ poster 自身にして、isFollowing に到達しないようにしている)。
func TestShouldFilterReplyShortCircuits(t *testing.T) {
	me := withTestConfig(t)
	ctx := context.Background()
	actor := Config.PrimaryActor()

	for _, tt := range []struct {
		name string
		note *activitystream.Object
	}{
		{name: "返信ではない", note: note("", nil, nil, "")},
		{name: "スレッドの続き (自分の投稿への自分自身の返信)", note: note(me+"/status/1", nil, nil, "")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			in := &activitystream.Object{Type: activitystream.CreateType, Actor: activitystream.URIRef(me)}
			if got := shouldFilterReply(ctx, actor, in, tt.note); got {
				t.Error("shouldFilterReply() = true, want false")
			}
		})
	}
}
