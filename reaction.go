package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/nna774/s.nna774.net/activitystream"
	"github.com/nna774/s.nna774.net/config"
	"github.com/nna774/s.nna774.net/datastore"
	"github.com/nna774/s.nna774.net/httperror"
)

// parseReactionRequest はいいね・ブーストのリクエストから対象投稿の URI と
// その著者の URI を取り出す。著者を呼び出し側から受けるのは、タイムライン
// が既にその情報を持っており、配信先を決めるためだけにもう一度リモートへ
// 取りに行かずに済むため。
func parseReactionRequest(r *http.Request) (object string, actor string, herr httperror.HttpError) {
	if isFormRequest(r) {
		if err := r.ParseForm(); err != nil {
			return "", "", httperror.StatusUnprocessableEntity("bad form", err)
		}
		object = strings.TrimSpace(r.PostFormValue("object"))
		actor = strings.TrimSpace(r.PostFormValue("actor"))
	} else {
		var body struct {
			Object string `json:"object"`
			Actor  string `json:"actor"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return "", "", httperror.StatusUnprocessableEntity("bad request", err)
		}
		object, actor = strings.TrimSpace(body.Object), strings.TrimSpace(body.Actor)
	}
	if object == "" {
		return "", "", httperror.StatusUnprocessableEntity("object must not be empty", nil)
	}
	if actor == "" {
		return "", "", httperror.StatusUnprocessableEntity("actor must not be empty", nil)
	}
	return object, actor, nil
}

func objectFromQuery(r *http.Request) (string, httperror.HttpError) {
	object := strings.TrimSpace(r.URL.Query().Get("object"))
	if object == "" {
		return "", httperror.StatusUnprocessableEntity("object query parameter is required", nil)
	}
	return object, nil
}

// likeRequestHandler は他人の投稿にいいねする。著者の inbox に直接届ける。
// primary actor 専用 (sub actor は following/likes/boosts を持たない)。
func likeRequestHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	ctx := r.Context()
	primary, herr := resolvePrimaryActor(r)
	if herr != nil {
		return herr
	}
	object, actorURI, herr := parseReactionRequest(r)
	if herr != nil {
		return herr
	}

	actor, err := fetchActor(ctx, primary, actorURI)
	if err != nil {
		return httperror.StatusUnprocessableEntity("cannot fetch that actor", err)
	}
	inbox := actor.InboxURI()
	if inbox == "" {
		return httperror.StatusUnprocessableEntity("actor advertises no inbox", nil)
	}

	like := activitystream.NewLike(newActivityID("like"), primary.ID(), object)
	// 表示名とアイコンも一緒に控える。/u/:user/favorites の一覧描画のたびに
	// 著者をリモートへ取りに行かずに済ませるため。
	name, iconURL := actorDisplay(actor)
	// 本文もこの時点でスナップショットしておく。あとで消えたり編集されたり
	// しても /u/:user/favorites に何も出せなくなるのを避けるため。引けなく
	// てもいいね自体は失敗させない。
	var content string
	if note, err := fetchVerifiedNote(ctx, primary, object); err == nil {
		content = note.Content
	} else {
		logf("cannot snapshot content of %v for favorites: %v", object, err)
	}
	// 配信より先に記録する。逆順だと、配信された Like を取り消す手段が
	// 無くなる。
	if err := client.PutKV(ctx, &datastore.KVItem{
		PK:                actorScoped(primary, datastore.KVMyLikes),
		SK:                object,
		ActivityID:        like.ID,
		Inbox:             inbox,
		TargetActor:       actorURI,
		Name:              name,
		IconURL:           iconURL,
		PreferredUsername: actor.PreferredUsername,
		Content:           content,
		At:                nowRFC3339(),
	}); err != nil {
		return httperror.StatusInternalServerError("cannot record the like", err)
	}
	if err := sendToInbox(ctx, primary, inbox, like); err != nil {
		return httperror.StatusInternalServerError("cannot deliver the Like", err)
	}
	return respondAsJSON(w, http.StatusAccepted, like)
}

// unlikeRequestHandler は Undo(Like) を送っていいねを取り消す。primary
// actor 専用。
func unlikeRequestHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	ctx := r.Context()
	primary, herr := resolvePrimaryActor(r)
	if herr != nil {
		return herr
	}
	object, herr := objectFromQuery(r)
	if herr != nil {
		return herr
	}

	item, err := client.GetKV(ctx, actorScoped(primary, datastore.KVMyLikes), object)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return httperror.StatusNotFound("not liked", err)
		}
		return httperror.StatusInternalServerError("cannot look up the like", err)
	}

	like := activitystream.NewLike(item.ActivityID, primary.ID(), object)
	undo := activitystream.NewUndo(like, primary.ID(), newActivityID("undo"))
	if item.Inbox != "" {
		if err := sendToInbox(ctx, primary, item.Inbox, undo); err != nil {
			logf("Undo(Like) to %v failed: %v", item.Inbox, err)
		}
	}
	if err := client.DeleteKV(ctx, actorScoped(primary, datastore.KVMyLikes), object); err != nil {
		return httperror.StatusInternalServerError("cannot remove the like", err)
	}
	return respondAsJSON(w, http.StatusOK, undo)
}

// boostRequestHandler は他人の投稿をブーストする。フォロワーに配信すると
// 同時に自分のタイムラインにも積む。表示は inbox.go の announceHandler が
// 受信したブーストを表示するのと同じ仕組み (timelineItem の BoostedBy*) を
// 使うため、埋め込む Note は受信時と同じ検証を経て取得する。
// boostRequestHandler は primary actor 専用。
func boostRequestHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	ctx := r.Context()
	primary, herr := resolvePrimaryActor(r)
	if herr != nil {
		return herr
	}
	object, actorURI, herr := parseReactionRequest(r)
	if herr != nil {
		return herr
	}

	note, err := fetchVerifiedNote(ctx, primary, object)
	if err != nil {
		return httperror.StatusUnprocessableEntity("cannot fetch that status", err)
	}
	cacheActorInfo(ctx, primary, note.AttributedTo.ID())

	announce := activitystream.NewAnnounce(
		newActivityID("announce"), primary.ID(), object,
		[]string{activitystream.ToPublic}, []string{followersURI(primary), actorURI})
	announce.Object = activitystream.ObjectRef(note)
	// timelineHandler はブーストの並び順にこの時刻を使う。空だと「解釈できない
	// published」として末尾に落ち、ブーストしたのに一番上に出てこなくなる。
	announce.Published = nowRFC3339()

	// 配信より先に保存する。逆順だと、配信されたのに自分のタイムラインには
	// 出ていないブーストができてしまう。
	timelineID, err := appendToTimelineWithID(ctx, announce)
	if err != nil {
		return httperror.StatusInternalServerError("cannot save the boost", err)
	}
	if err := client.PutKV(ctx, &datastore.KVItem{
		PK:          actorScoped(primary, datastore.KVMyBoosts),
		SK:          object,
		ActivityID:  announce.ID,
		TargetActor: actorURI,
		TimelineID:  timelineID,
		At:          nowRFC3339(),
	}); err != nil {
		return httperror.StatusInternalServerError("cannot record the boost", err)
	}
	// announceStatusHandler が Announce.ID (/announce/:id) から辿るための
	// 逆引き。KVMyBoosts は対象投稿の URI がキーなので、これが無いと
	// Announce 自身の URI からは引けない。
	if err := client.PutKV(ctx, &datastore.KVItem{
		PK:         actorScoped(primary, datastore.KVMyBoostByID),
		SK:         announce.ID,
		TimelineID: timelineID,
	}); err != nil {
		return httperror.StatusInternalServerError("cannot record the boost", err)
	}

	inboxes, err := followerInboxes(ctx, primary)
	if err != nil {
		return httperror.StatusInternalServerError("cannot list follower inboxes", err)
	}
	// 著者はフォロワーでないことが多いが、ブーストされたことは知らせる必要が
	// ある。postStatusHandler の mention 配信と同じ理由。
	if a, err := fetchActor(ctx, primary, actorURI); err == nil {
		if inbox := a.InboxURI(); inbox != "" {
			inboxes = appendUnique(inboxes, inbox)
		}
	} else {
		logf("cannot fetch the author %v of the boosted status: %v", actorURI, err)
	}
	if err := deliver(ctx, primary, inboxes, announce); err != nil {
		logf("boost %v had delivery failures: %v", announce.ID, err)
	}
	return respondAsJSON(w, http.StatusAccepted, announce)
}

// unboostRequestHandler は Undo(Announce) を送ってブーストを取り消し、
// 自分のタイムラインからも外す。primary actor 専用。
func unboostRequestHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	ctx := r.Context()
	primary, herr := resolvePrimaryActor(r)
	if herr != nil {
		return herr
	}
	object, herr := objectFromQuery(r)
	if herr != nil {
		return herr
	}

	item, err := client.GetKV(ctx, actorScoped(primary, datastore.KVMyBoosts), object)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return httperror.StatusNotFound("not boosted", err)
		}
		return httperror.StatusInternalServerError("cannot look up the boost", err)
	}

	announce := activitystream.NewAnnounce(item.ActivityID, primary.ID(), object, nil, nil)
	undo := activitystream.NewUndo(announce, primary.ID(), newActivityID("undo"))

	inboxes, err := followerInboxes(ctx, primary)
	if err != nil {
		logf("cannot list follower inboxes for Undo(Announce): %v", err)
	}
	if item.TargetActor != "" {
		if a, err := fetchActor(ctx, primary, item.TargetActor); err == nil {
			if inbox := a.InboxURI(); inbox != "" {
				inboxes = appendUnique(inboxes, inbox)
			}
		}
	}
	if err := deliver(ctx, primary, inboxes, undo); err != nil {
		logf("Undo(Announce) had delivery failures: %v", err)
	}

	if item.TimelineID != 0 {
		if err := client.DeleteObject(ctx, timelineKey, item.TimelineID); err != nil {
			logf("removing the boost from the timeline failed: %v", err)
		}
	}
	if item.ActivityID != "" {
		if err := client.DeleteKV(ctx, actorScoped(primary, datastore.KVMyBoostByID), item.ActivityID); err != nil {
			logf("removing the boost id index failed: %v", err)
		}
	}
	if err := client.DeleteKV(ctx, actorScoped(primary, datastore.KVMyBoosts), object); err != nil {
		return httperror.StatusInternalServerError("cannot remove the boost", err)
	}
	return respondAsJSON(w, http.StatusOK, undo)
}

// reactionState は自分がいいね・ブースト済みの投稿 URI の集合。timeline の
// 描画のたびに投稿ごとへ問い合わせるのではなく、パーティション全体を1回
// ずつ引いて集合を作る。
type reactionState struct {
	liked   map[string]bool
	boosted map[string]bool
}

// reactorsOf は自分の投稿 (KVLikes / KVAnnounced) に対して、誰が反応したかを
// 返す。両パーティションとも SK が「対象投稿の URI + "#" + actor の URI」の
// 形で、投稿ごとに分かれていないため、pk 全体を引いてから prefix で絞る。
// 1人用インスタンスの反応数はたかが知れているので、これで十分。
func reactorsOf(ctx context.Context, pk, objectURI string) ([]*datastore.KVItem, error) {
	items, err := client.QueryKV(ctx, pk)
	if err != nil {
		return nil, err
	}
	prefix := objectURI + "#"
	result := make([]*datastore.KVItem, 0, len(items))
	for _, it := range items {
		if actorID, ok := strings.CutPrefix(it.SK, prefix); ok && actorID != "" {
			result = append(result, it)
		}
	}
	return result, nil
}

func loadReactionState(ctx context.Context, actor *config.ActorConfig) reactionState {
	state := reactionState{liked: map[string]bool{}, boosted: map[string]bool{}}
	if items, err := client.QueryKV(ctx, actorScoped(actor, datastore.KVMyLikes)); err != nil {
		logf("loading liked statuses failed: %v", err)
	} else {
		for _, it := range items {
			state.liked[it.SK] = true
		}
	}
	if items, err := client.QueryKV(ctx, actorScoped(actor, datastore.KVMyBoosts)); err != nil {
		logf("loading boosted statuses failed: %v", err)
	} else {
		for _, it := range items {
			state.boosted[it.SK] = true
		}
	}
	return state
}
