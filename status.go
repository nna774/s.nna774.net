package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/julienschmidt/httprouter"
	"github.com/nna774/s.nna774.net/activitystream"
	"github.com/nna774/s.nna774.net/datastore"
	"github.com/nna774/s.nna774.net/httperror"
)

// 公開範囲。ActivityPub には可視性という概念は無く、to / cc に
// Public とフォロワーコレクションをどう置くかで表現する。
const (
	visibilityPublic    = "public"
	visibilityUnlisted  = "unlisted"
	visibilityFollowers = "followers"
)

// statusRequest は投稿エンドポイントが受ける入力。JSON と form の
// 両方から同じ形に落とす。
type statusRequest struct {
	Content    string   `json:"content"`
	Visibility string   `json:"visibility"`
	InReplyTo  string   `json:"in_reply_to"`
	Mentions   []string `json:"mentions"`
}

func (req *statusRequest) normalize() error {
	req.Content = strings.TrimSpace(req.Content)
	if req.Content == "" {
		return errors.New("content must not be empty")
	}
	switch req.Visibility {
	case "":
		req.Visibility = visibilityPublic
	case visibilityPublic, visibilityUnlisted, visibilityFollowers:
	default:
		return fmt.Errorf("unknown visibility %q", req.Visibility)
	}
	return nil
}

// audience は公開範囲から to / cc を決める。
func (req *statusRequest) audience(followers string) (to []string, cc []string) {
	switch req.Visibility {
	case visibilityUnlisted:
		// 公開タイムラインには載らないが、URL を知れば誰でも見られる。
		to = []string{followers}
		cc = []string{activitystream.ToPublic}
	case visibilityFollowers:
		to = []string{followers}
	default:
		to = []string{activitystream.ToPublic}
		cc = []string{followers}
	}
	// mention 先は actor の URI を cc に入れる。inbox の URI を入れるのは
	// 誤りで、以前の実装はそうなっていた。
	cc = append(cc, req.Mentions...)
	return to, cc
}

func parseStatusRequest(r *http.Request) (*statusRequest, error) {
	req := &statusRequest{}
	if isFormRequest(r) {
		if err := r.ParseForm(); err != nil {
			return nil, err
		}
		req.Content = r.PostFormValue("content")
		req.Visibility = r.PostFormValue("visibility")
		req.InReplyTo = r.PostFormValue("in_reply_to")
		if m := strings.TrimSpace(r.PostFormValue("mentions")); m != "" {
			req.Mentions = strings.Fields(m)
		}
	} else if err := json.NewDecoder(r.Body).Decode(req); err != nil {
		return nil, err
	}
	if err := req.normalize(); err != nil {
		return nil, err
	}
	return req, nil
}

func isFormRequest(r *http.Request) bool {
	ct := r.Header.Get("Content-Type")
	return strings.HasPrefix(ct, "application/x-www-form-urlencoded") ||
		strings.HasPrefix(ct, "multipart/form-data")
}

// postStatusHandler は新しい Note を作り、保存してフォロワーに配信する。
func postStatusHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	ctx := r.Context()

	req, err := parseStatusRequest(r)
	if err != nil {
		return httperror.StatusUnprocessableEntity("bad status request", err)
	}

	id, err := client.Inc(ctx, statusKey)
	if err != nil {
		return httperror.StatusInternalServerError("cannot allocate a status id", err)
	}
	to, cc := req.audience(followersURI())

	tags := make([]*activitystream.Object, 0, len(req.Mentions))
	for _, m := range req.Mentions {
		tags = append(tags, activitystream.NewMention(mentionName(ctx, m), m))
	}

	note := activitystream.NewNote(
		myStatusURI(id),
		// ActivityStreams の published は xsd:dateTime である。HTTP の
		// Date ヘッダ書式 (RFC1123) を流用してはならない。以前の実装は
		// そうなっていた。
		time.Now().UTC().Format(time.RFC3339),
		"", req.Content, Config.ID(), to, cc, tags)
	if req.InReplyTo != "" {
		note.InReplyTo = activitystream.URIRef(req.InReplyTo)
	}
	create := noteToCreate(note)

	// 配信より先に保存する。逆順だと、配信されたのに自分の outbox には
	// 無い投稿ができてしまう。
	if err := saveStatus(ctx, id, note); err != nil {
		return httperror.StatusInternalServerError("cannot save the status", err)
	}
	if err := saveToOutbox(ctx, id, create); err != nil {
		return httperror.StatusInternalServerError("cannot save to the outbox", err)
	}

	inboxes, err := followerInboxes(ctx)
	if err != nil {
		return httperror.StatusInternalServerError("cannot list follower inboxes", err)
	}
	// mention 先はフォロワーでなくても届ける必要がある。
	for _, m := range req.Mentions {
		actor, err := fetchActor(ctx, m)
		if err != nil {
			logf("cannot fetch mentioned actor %v: %v", m, err)
			continue
		}
		if inbox := actor.InboxURI(); inbox != "" {
			inboxes = appendUnique(inboxes, inbox)
		}
	}

	deliveryErr := deliver(ctx, inboxes, create)
	if deliveryErr != nil {
		// 保存は済んでいるので投稿自体は成立している。失敗した宛先だけ
		// 報告する。
		logf("status %v was saved but delivery had failures: %v", note.ID, deliveryErr)
	}

	if isFormRequest(r) {
		// リロードで二重投稿しないよう 303 でタイムラインに戻す。
		http.Redirect(w, r, "/timeline", http.StatusSeeOther)
		return nil
	}
	return respondAsJSON(w, http.StatusCreated, create)
}

// deleteStatusHandler は投稿を消し、Delete を配信する。
func deleteStatusHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	ctx := r.Context()
	id, herr := statusIDFromRequest(r)
	if herr != nil {
		return herr
	}

	note, err := client.GetObject(ctx, statusKey, id)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return httperror.StatusNotFound("no such status", err)
		}
		return httperror.StatusInternalServerError("cannot load the status", err)
	}

	del := activitystream.NewDelete(newActivityID("delete"), Config.ID(), note.To, note.ID)
	inboxes, err := followerInboxes(ctx)
	if err != nil {
		return httperror.StatusInternalServerError("cannot list follower inboxes", err)
	}
	if err := deliver(ctx, inboxes, del); err != nil {
		logf("Delete of %v had delivery failures: %v", note.ID, err)
	}

	if err := client.DeleteObject(ctx, statusKey, id); err != nil {
		return httperror.StatusInternalServerError("cannot delete the status", err)
	}
	if err := client.DeleteObject(ctx, outboxKey, id); err != nil {
		logf("removing %v from the outbox failed: %v", note.ID, err)
	}

	if isFormRequest(r) {
		http.Redirect(w, r, "/timeline", http.StatusSeeOther)
		return nil
	}
	return respondAsJSON(w, http.StatusOK, del)
}

// followRequestHandler は自分から相手をフォローする。タイムラインに
// 中身を入れるために必要。
func followRequestHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	ctx := r.Context()

	target := ""
	if isFormRequest(r) {
		if err := r.ParseForm(); err != nil {
			return httperror.StatusUnprocessableEntity("bad form", err)
		}
		target = strings.TrimSpace(r.PostFormValue("actor"))
	} else {
		var body struct {
			Actor string `json:"actor"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return httperror.StatusUnprocessableEntity("bad request", err)
		}
		target = strings.TrimSpace(body.Actor)
	}
	if target == "" {
		return httperror.StatusUnprocessableEntity("actor must not be empty", nil)
	}
	// URI でも @user@host でも受ける。
	target, err := resolveActorURI(ctx, target)
	if err != nil {
		return httperror.StatusUnprocessableEntity("cannot resolve that actor", err)
	}

	actor, err := fetchActor(ctx, target)
	if err != nil {
		return httperror.StatusUnprocessableEntity("cannot fetch that actor", err)
	}
	inbox := actor.InboxURI()
	if inbox == "" {
		return httperror.StatusUnprocessableEntity(fmt.Sprintf("actor %v advertises no inbox", target), nil)
	}

	follow := activitystream.NewFollow(newActivityID("follow"), Config.ID(), actor.ID)
	// 相手が Accept を返してきたときに突き合わせられるよう、送る前に
	// pending で記録する。
	if err := saveFollower(ctx, datastore.KVFollowing, actor, follow.ID, datastore.FollowStatePending); err != nil {
		return httperror.StatusInternalServerError("cannot record the follow", err)
	}
	if err := sendToInbox(ctx, inbox, follow); err != nil {
		return httperror.StatusInternalServerError("cannot deliver the Follow", err)
	}

	if isFormRequest(r) {
		http.Redirect(w, r, "/timeline", http.StatusSeeOther)
		return nil
	}
	return respondAsJSON(w, http.StatusAccepted, follow)
}

// unfollowRequestHandler は Undo(Follow) を送ってフォローを解除する。
func unfollowRequestHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	ctx := r.Context()
	target := strings.TrimSpace(r.URL.Query().Get("actor"))
	if target == "" {
		return httperror.StatusUnprocessableEntity("actor query parameter is required", nil)
	}

	item, err := client.GetKV(ctx, datastore.KVFollowing, target)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return httperror.StatusNotFound("not following that actor", err)
		}
		return httperror.StatusInternalServerError("cannot look up the follow", err)
	}

	follow := activitystream.NewFollow(item.ActivityID, Config.ID(), target)
	undo := activitystream.NewUndo(follow, Config.ID(), newActivityID("undo"))

	inbox := item.Inbox
	if inbox == "" {
		inbox = item.SharedInbox
	}
	if inbox != "" {
		if err := sendToInbox(ctx, inbox, undo); err != nil {
			logf("Undo(Follow) to %v failed: %v", inbox, err)
		}
	}
	if err := client.DeleteKV(ctx, datastore.KVFollowing, target); err != nil {
		return httperror.StatusInternalServerError("cannot remove the follow", err)
	}
	return respondAsJSON(w, http.StatusOK, undo)
}

func statusIDFromRequest(r *http.Request) (int, httperror.HttpError) {
	params := httprouter.ParamsFromContext(r.Context())
	idStr := params.ByName("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return 0, httperror.StatusUnprocessableEntity(fmt.Sprintf("bad status id: %v", idStr), err)
	}
	return id, nil
}

// mentionName は @user@host 形式の表記を組む。actor が引けなければ
// URI をそのまま使う。
func mentionName(ctx context.Context, actorURI string) string {
	actor, err := fetchActor(ctx, actorURI)
	if err != nil || actor.PreferredUsername == "" {
		return actorURI
	}
	host := hostOf(actorURI)
	if host == "" {
		return "@" + actor.PreferredUsername
	}
	return "@" + actor.PreferredUsername + "@" + host
}

func appendUnique(xs []string, v string) []string {
	for _, x := range xs {
		if x == v {
			return xs
		}
	}
	return append(xs, v)
}
