package main

import (
	"context"
	"net/http"
	"time"

	"github.com/nna774/s.nna774.net/activitystream"
	"github.com/nna774/s.nna774.net/datastore"
	"github.com/nna774/s.nna774.net/httperror"
)

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339) }

// saveFollower はフォロワー / フォロー中を KV に記録する。表示名と
// アイコンも一緒に持つのは、タイムライン表示のたびにリモートへ actor を
// 取りに行かずに済ませるため。
func saveFollower(ctx context.Context, partition string, actor *activitystream.Object, activityID string, state string) error {
	name, iconURL := actorDisplay(actor)
	return client.PutKV(ctx, &datastore.KVItem{
		PK:          partition,
		SK:          actor.ID,
		Inbox:       actor.Inbox,
		SharedInbox: sharedInboxOf(actor),
		Name:        name,
		IconURL:     iconURL,
		ActivityID:  activityID,
		State:       state,
		At:          nowRFC3339(),
	})
}

// actorDisplay は actor の表示名とアイコン URL を取り出す。name が無い
// 実装があるので preferredUsername に落とす。
func actorDisplay(actor *activitystream.Object) (name string, iconURL string) {
	if actor.Icon != nil {
		iconURL = actor.Icon.URL
	}
	name = actor.Name
	if name == "" {
		name = actor.PreferredUsername
	}
	return name, iconURL
}

func sharedInboxOf(actor *activitystream.Object) string {
	if actor.Endpoints != nil {
		return actor.Endpoints.SharedInbox
	}
	return ""
}

// followerInboxes は配信先の一覧を返す。sharedInbox を持つ相手は
// そこにまとめ、同じ inbox への重複を排除する。
func followerInboxes(ctx context.Context) ([]string, error) {
	items, err := client.QueryKV(ctx, datastore.KVFollowers)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	inboxes := make([]string, 0, len(items))
	for _, it := range items {
		// 保留中のフォロー要求には配信しない。
		if it.State != datastore.FollowStateAccepted {
			continue
		}
		target := it.SharedInbox
		if target == "" {
			target = it.Inbox
		}
		if target == "" || seen[target] {
			continue
		}
		seen[target] = true
		inboxes = append(inboxes, target)
	}
	return inboxes, nil
}

// collectionHandler は followers / following を返す。ActivityPub の
// クライアントには OrderedCollection の JSON を、ブラウザには誰がいるのか
// 読める HTML の一覧を出し分ける。中身は出さず件数とページへのリンクだけを
// 返す実装もあるが、1人用なので items をそのまま並べる。
func collectionHandler(partition string, uri func() string, heading string) func(http.ResponseWriter, *http.Request) httperror.HttpError {
	return func(w http.ResponseWriter, r *http.Request) httperror.HttpError {
		ctx := r.Context()
		// 同じ URI が Accept によって JSON と HTML を返すので、キャッシュに
		// 混ざらないよう Vary を付ける。
		w.Header().Set("Vary", "Accept")
		if Config.HideCollections {
			// 件数も中身も出さない。連合には totalItems 0 の空コレクション、
			// ブラウザには空の一覧を返す。
			if wantsActivityJSON(r) {
				return respondAsJSON(w, http.StatusOK,
					activitystream.NewOrderedCollection(uri(), 0, "", ""))
			}
			return htmlCollectionHandler(w, r, nil, heading)
		}
		items, err := client.QueryKV(ctx, partition)
		if err != nil {
			return httperror.StatusInternalServerError("cannot list the collection", err)
		}
		accepted := make([]*datastore.KVItem, 0, len(items))
		for _, it := range items {
			if it.State != datastore.FollowStateAccepted {
				continue
			}
			accepted = append(accepted, it)
		}
		if !wantsActivityJSON(r) {
			return htmlCollectionHandler(w, r, accepted, heading)
		}
		ids := make([]string, 0, len(accepted))
		for _, it := range accepted {
			ids = append(ids, it.SK)
		}
		return respondAsJSON(w, http.StatusOK, activitystream.NewOrderedCollectionOfIDs(uri(), ids))
	}
}
