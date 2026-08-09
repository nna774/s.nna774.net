package main

import (
	"context"
	"net/http"
	"strings"

	"github.com/nna774/s.nna774.net/activitystream"
	"github.com/nna774/s.nna774.net/config"
	"github.com/nna774/s.nna774.net/datastore"
	"github.com/nna774/s.nna774.net/httperror"
)

// remoteStatusesLimit はリモートプロフィールに出す投稿の件数の上限。
// 自分のプロフィールの profileStatusCount に合わせる。
const remoteStatusesLimit = profileStatusCount

type remoteStatusItem struct {
	Content     string
	Published   string
	ObjectURI   string
	InReplyTo   string
	Attachments []attachmentItem
}

type remoteProfilePage struct {
	pageBase
	// Query は検索欄に残す入力値。ハンドルでも URL でも受ける。
	Query string
	Error string
	// Found が立っているときだけプロフィール本体を描画する。
	Found bool

	Name        string
	ActorURI    string
	IconURL     string
	Summary     string
	StatusCount int
	Statuses    []remoteStatusItem

	// Following / FollowPending は自分から見たフォロー状態。ボタンの
	// 出し分けに使う。両方偽ならまだフォローしていない。
	Following     bool
	FollowPending bool
}

// remoteProfileHandler はリモートの actor をハンドルまたは URL で引き、
// プロフィールと最近の投稿を表示する。フォロー/フォロー解除もここから
// 行える — 別インスタンスへ飛ばずに相手を確かめてからフォローしたい、
// というのがこのページの動機である。
func remoteProfileHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	ctx := r.Context()
	primary := Config.PrimaryActor()
	query := strings.TrimSpace(r.URL.Query().Get("actor"))

	page := remoteProfilePage{
		pageBase: newPageBase(r, "アカウントを見る"),
		Query:    query,
	}
	page.UnreadCount = len(unreadNotifications(ctx))
	// ハンドル入力欄を含む私用ページなので検索避けする。
	page.NoIndex = true

	if query == "" {
		return renderPage(w, "remote", page)
	}

	actorURI, err := resolveActorURI(ctx, query)
	if err != nil {
		page.Error = "そのハンドルまたは URL を解決できなかった: " + err.Error()
		return renderPage(w, "remote", page)
	}
	actor, err := fetchActor(ctx, primary, actorURI)
	if err != nil {
		page.Error = "相手を取得できなかった: " + err.Error()
		return renderPage(w, "remote", page)
	}

	page.Found = true
	page.ActorURI = actor.ID
	page.Name, page.IconURL = actorDisplay(actor)
	page.Summary = actor.Summary
	page.Statuses, page.StatusCount = remoteRecentStatuses(ctx, primary, actor)

	switch followStateOf(ctx, primary, actor.ID) {
	case datastore.FollowStateAccepted:
		page.Following = true
	case datastore.FollowStatePending:
		page.FollowPending = true
	}

	return renderPage(w, "remote", page)
}

// followStateOf は自分から見た actorURI へのフォロー状態を返す。
// フォローしていなければ空文字。
func followStateOf(ctx context.Context, primary *config.ActorConfig, actorURI string) string {
	item, err := client.GetKV(ctx, actorScoped(primary, datastore.KVFollowing), actorURI)
	if err != nil {
		return ""
	}
	return item.State
}

// remoteRecentStatuses は outbox の先頭ページから投稿を取り出す。
// 取れなくてもプロフィール自体は出したいので、ここでの失敗は
// 空の一覧に落とすだけで致命的にはしない。
func remoteRecentStatuses(ctx context.Context, requester *config.ActorConfig, actor *activitystream.Object) ([]remoteStatusItem, int) {
	if actor.Outbox == "" {
		return nil, 0
	}
	outbox, err := fetchObject(ctx, requester, actor.Outbox)
	if err != nil {
		logf("fetching the outbox of %v failed: %v", actor.ID, err)
		return nil, 0
	}
	total := 0
	if outbox.TotalItems != nil {
		total = *outbox.TotalItems
	}
	if outbox.First == "" {
		return nil, total
	}
	first, err := fetchObject(ctx, requester, outbox.First)
	if err != nil {
		logf("fetching the first outbox page of %v failed: %v", actor.ID, err)
		return nil, total
	}

	items := make([]remoteStatusItem, 0, remoteStatusesLimit)
	for _, ref := range first.OrderedItems {
		if len(items) >= remoteStatusesLimit {
			break
		}
		item := ref.Item()
		if item == nil {
			// 素の URI だけの要素は都度取りに行くほどのことではない。
			continue
		}
		note := item
		if item.Type == activitystream.CreateType || item.Type == activitystream.UpdateType {
			note = item.Object.Item()
		}
		if note == nil || note.Content == "" {
			continue
		}
		items = append(items, remoteStatusItem{
			Content:     note.Content,
			Published:   note.Published,
			ObjectURI:   note.ID,
			InReplyTo:   note.InReplyTo.ID(),
			Attachments: noteAttachments(note),
		})
	}
	return items, total
}
