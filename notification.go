package main

import (
	"context"
	"errors"

	"github.com/nna774/s.nna774.net/activitystream"
	"github.com/nna774/s.nna774.net/config"
	"github.com/nna774/s.nna774.net/datastore"
)

// notificationKey は通知を積む連番キー。中身は受信した Activity そのもの
// で、表示側が type を見て「いいね」「ブースト」「返信」「フォロー」に
// 振り分ける。専用の型を作らないのは、必要な情報 (誰が・何に・いつ) が
// すべて Activity に載っているため。
const notificationKey = "notification"

// notificationScanLimit は通知を遡って探すときの上限。連番キーのテーブル
// は id で直接引けないので走査するしかない。タイムラインの削除と同じ
// 妥協である。
const notificationScanLimit = 500

// unreadCountLimit は未読数を数えるときの打ち切り。これを超えたら
// 「99+」相当の扱いでよく、全件数える必要は無い。
const unreadCountLimit = 100

// isMyStatus は URI がローカルのいずれかの Actor (primary / sub 問わず) の
// 投稿かを返す。
func isMyStatus(uri string) bool {
	_, _, ok := actorAndIDFromStatusURI(uri)
	return ok
}

// appendNotification は Activity を通知として積む。
func appendNotification(ctx context.Context, in *activitystream.Object) error {
	id, err := client.Inc(ctx, notificationKey)
	if err != nil {
		return err
	}
	// published は受信時刻で上書きする。
	//
	// Like と Follow はそもそも published を持たないことが多い。加えて、
	// 通知は連番の降順 (受信順) に並べるので、リモートが付けた投稿時刻を
	// 表示すると配信の遅れた通知だけ時刻が前後して並びと食い違う。元の
	// 投稿時刻は通知から辿れる投稿の側にある。
	//
	// 元の Activity は書き換えず値をコピーする。同じポインタをタイムライン
	// にも積むことがあり、そちらに混ざると保存済みの中身と食い違う。
	act := *in
	act.Published = nowRFC3339()
	return client.Put(ctx, notificationKey, id, &act)
}

// notifyOrLog は通知の保存に失敗しても呼び出し側を失敗させない。通知は
// 副産物であり、これで inbox が 5xx を返すと相手が本体の Activity ごと
// リトライしてくる。
func notifyOrLog(ctx context.Context, actor *config.ActorConfig, in *activitystream.Object) {
	// 表示名とアイコンを受信時に控える。フォロワーでない相手からの通知は
	// これが無いと actor の URI がそのまま並ぶ。
	cacheActorInfo(ctx, actor, in.Actor.ID())
	if err := appendNotification(ctx, in); err != nil {
		logf("recording a %v notification from %v failed: %v", in.Type, in.Actor.ID(), err)
	}
}

// notifiedObject は対象の投稿が自分宛として通知に載っているかを返す。
//
// 削除を通知にするかの判定に使う。Mastodon は投稿を消すたびに Delete を
// フォロワー全員に配信するため、届いた Delete をすべて通知にすると
// フォロー相手の消した独り言まで並んでしまう。
func notifiedObject(ctx context.Context, objectURI string) bool {
	if objectURI == "" {
		return false
	}
	entries, err := client.TakeEntries(ctx, notificationKey, datastore.Inf, notificationScanLimit, datastore.Desc)
	if err != nil {
		logf("looking for a notification of %v failed: %v", objectURI, err)
		return false
	}
	for _, e := range entries {
		// 通知に載っている Create の中の Note と突き合わせる。
		if inner := e.Object.Object.Item(); inner != nil && inner.ID == objectURI {
			return true
		}
	}
	return false
}

// notifiesMe は受信した Create / Update が自分宛かを返す。
//
// 自分の投稿への返信、to / cc に自分が入っているもの、本文の Mention タグ
// が自分を指しているものが該当する。フォロー相手同士の会話はタイムライン
// に流すだけで通知にはしない。
func notifiesMe(actor *config.ActorConfig, in *activitystream.Object, note *activitystream.Object) bool {
	if isMyStatus(note.InReplyTo.ID()) {
		return true
	}
	me := actor.ID()
	// 宛先は Activity 側にも Note 側にも書かれる。実装によってどちらに
	// 入るかが違うので両方見る。
	for _, addrs := range []activitystream.Strings{in.To, in.Cc, note.To, note.Cc} {
		for _, addr := range addrs {
			if addr == me {
				return true
			}
		}
	}
	for _, tag := range note.Tag {
		if tag != nil && tag.Type == activitystream.MentionType && tag.Href == me {
			return true
		}
	}
	return false
}

// readCursor は「どこまで読んだか」の連番を返す。一度も読んでいなければ 0。
func readCursor(ctx context.Context, name string) (int, error) {
	it, err := client.GetKV(ctx, datastore.KVCursor, name)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return 0, nil
		}
		return 0, err
	}
	return it.Cursor, nil
}

// advanceReadCursor は既読位置を進める。戻すことはしない。古い通知を
// 見たときに巻き戻すと、既に読んだものが未読に戻る。
func advanceReadCursor(ctx context.Context, name string, to int) error {
	cur, err := readCursor(ctx, name)
	if err != nil {
		return err
	}
	if to <= cur {
		return nil
	}
	return client.PutKV(ctx, &datastore.KVItem{
		PK:     datastore.KVCursor,
		SK:     name,
		Cursor: to,
		At:     nowRFC3339(),
	})
}

// unreadNotifications は既読位置より新しい通知の連番を集合で返す。
//
// 件数を Inc の値と既読位置の差で求めることもできるが、Undo で通知を
// 消しても Inc の値は減らないため実際の件数と合わなくなる。実際に残って
// いるものを引く。
func unreadNotifications(ctx context.Context) map[int]bool {
	cursor, err := readCursor(ctx, notificationKey)
	if err != nil {
		logf("reading the notification cursor failed: %v", err)
		return nil
	}
	entries, err := client.TakeEntries(ctx, notificationKey, cursor+1, unreadCountLimit, datastore.Asc)
	if err != nil && !errors.Is(err, datastore.ErrNotFound) {
		logf("counting unread notifications failed: %v", err)
		return nil
	}
	unread := make(map[int]bool, len(entries))
	for _, e := range entries {
		unread[e.ID] = true
	}
	return unread
}
