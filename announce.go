package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/julienschmidt/httprouter"
	"github.com/nna774/s.nna774.net/activitystream"
	"github.com/nna774/s.nna774.net/datastore"
	"github.com/nna774/s.nna774.net/httperror"
)

// announceStatusHandler は自分のブースト (Announce) を id から表示する。
// Announce.ID は newActivityID で origin/announce/<nano> という URI に
// なっているが、これを取得するエンドポイントが無く、リンク切れの URI
// だった。KVMyBoostByID (boostRequestHandler が記録) で Announce.ID から
// タイムラインの連番を引き、保存済みの Announce を返す。
//
// 受信したブースト (他インスタンスの Announce) の ID はこちらの発行では
// ないので対象外。foreign な URI は相手のサーバが返すべきものであり、
// ここでは自分が出した Announce だけを扱う。
func announceStatusHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	w.Header().Set("Vary", "Accept")
	ctx := r.Context()
	id := httprouter.ParamsFromContext(ctx).ByName("id")
	announceURI := fmt.Sprintf("%s/announce/%s", Config.Origin, id)

	item, err := client.GetKV(ctx, datastore.KVMyBoostByID, announceURI)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return httperror.StatusNotFound("no such boost", err)
		}
		return httperror.StatusInternalServerError("cannot look up the boost", err)
	}
	announce, err := client.GetObject(ctx, timelineKey, item.TimelineID)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return httperror.StatusNotFound("no such boost", err)
		}
		return httperror.StatusInternalServerError("cannot load the boost", err)
	}
	if !wantsActivityJSON(r) {
		return htmlAnnounceHandler(w, r, announce)
	}
	return respondAsJSON(w, http.StatusOK, announce)
}

type announcePage struct {
	pageBase
	AnnounceURI string
	Name        string
	AuthorURI   string
	IconURL     string
	// Mine はブーストした投稿の著者が自分自身かどうか。自分の投稿を
	// 自分でブーストした場合に、著者名をリンクにしないための出し分けに使う。
	Mine        bool
	Content     string
	Attachments []attachmentItem
	Published   string
	InReplyTo   string
	ObjectURI   string
	Excerpt     string
	Liked       bool
}

func htmlAnnounceHandler(w http.ResponseWriter, r *http.Request, announce *activitystream.Object) httperror.HttpError {
	note := announce.Object.Item()
	if note == nil {
		return httperror.StatusInternalServerError("boosted note is missing", nil)
	}
	ctx := r.Context()
	authorURI := note.AttributedTo.ID()
	page := announcePage{
		pageBase:    newPageBase(r, Config.Name+" のブースト: "+excerpt(note.Content, 40)),
		AnnounceURI: announce.ID,
		Name:        authorName(ctx, authorURI),
		AuthorURI:   authorURI,
		IconURL:     cachedIconURL(ctx, authorURI),
		Mine:        authorURI == Config.ID(),
		Content:     note.Content,
		Attachments: noteAttachments(note),
		Published:   announce.Published,
		InReplyTo:   note.InReplyTo.ID(),
		ObjectURI:   note.ID,
		Excerpt:     excerpt(note.Content, 140),
		Liked:       isLiked(ctx, note.ID),
	}
	return renderPage(w, "announce", page)
}

// isLiked はこの投稿にすでにいいねしているかどうかを1件だけ引く。
// timeline のように一覧全体を並べるページでは reactionState でまとめて
// 引くが、ここは1件しか要らないので個別に引く。
func isLiked(ctx context.Context, objectURI string) bool {
	_, err := client.GetKV(ctx, datastore.KVMyLikes, objectURI)
	return err == nil
}
