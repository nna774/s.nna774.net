package main

import (
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

	if err := verifyBoostedNote(valid(), uri); err != nil {
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
			if err := verifyBoostedNote(note, uri); err == nil {
				t.Error("弾かれなかった")
			}
		})
	}
}
