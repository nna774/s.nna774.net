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
func (req *statusRequest) audience(followers string, mentioned []string) (to []string, cc []string) {
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
	cc = append(cc, mentioned...)
	return to, cc
}

func parseStatusRequest(r *http.Request) (*statusRequest, error) {
	req := &statusRequest{}
	if isFormRequest(r) {
		// multipart/form-data は r.ParseForm() だけではボディを読まず
		// PostForm が空のままになる (ファイル部分と共にボディを読むには
		// ParseMultipartForm が要る)。画像添付の input[type=file] を
		// 足す前は素の form しか来なかったので気づかれていなかった。
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			if err := r.ParseMultipartForm(maxImageUploadBytes); err != nil {
				return nil, err
			}
		} else if err := r.ParseForm(); err != nil {
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

// maxImageUploadBytes は添付画像を multipart/form-data から読む際にメモリ
// 上に置く上限。API Gateway 自体のペイロード上限 (10MB) より小さくして
// あるので、画像はディスクに逃げずにメモリ内で完結する。
const maxImageUploadBytes = 8 << 20

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

	// 画像は id 発行より前に済ませる。アップロードに失敗した投稿のために
	// 連番を無駄に消費しないため。
	attachment, herr := imageAttachmentFromRequest(ctx, r)
	if herr != nil {
		return herr
	}

	id, err := client.Inc(ctx, statusKey)
	if err != nil {
		return httperror.StatusInternalServerError("cannot allocate a status id", err)
	}

	// 本文中の @user@host も明示指定もまとめて解決する。
	mentions := collectMentions(ctx, req.Content, req.Mentions)
	to, cc := req.audience(followersURI(), mentionURIs(mentions))

	note := activitystream.NewNote(
		myStatusURI(id),
		// ActivityStreams の published は xsd:dateTime である。HTTP の
		// Date ヘッダ書式 (RFC1123) を流用してはならない。以前の実装は
		// そうなっていた。
		time.Now().UTC().Format(time.RFC3339),
		"", renderContent(req.Content, mentions), Config.ID(), to, cc, mentionTags(mentions))
	if req.InReplyTo != "" {
		note.InReplyTo = activitystream.URIRef(req.InReplyTo)
	}
	if attachment != nil {
		note.Attachment = activitystream.Objects{attachment}
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
	// mention 先はフォロワーでなくても届ける必要がある。フォローされて
	// いない相手に話しかけられるのはこの経路だけである。
	for _, m := range mentions {
		actor, err := fetchActor(ctx, m.ActorURI)
		if err != nil {
			logf("cannot fetch mentioned actor %v: %v", m.ActorURI, err)
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
	// 配送に失敗したのにローカルの記録だけ消すと、相手には follow が生きた
	// ままなのにこちらは「解除した」つもりになる。しかも target のレコードが
	// 無いのでこのハンドラ自体を二度と呼べず、二度と解除できなくなる。
	// 届くまでローカルの記録を残し、失敗はエラーとして呼び出し元に返す。
	if inbox != "" {
		if err := sendToInbox(ctx, inbox, undo); err != nil {
			return httperror.StatusInternalServerError("cannot deliver the Undo(Follow)", err)
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

// imageAttachmentFromRequest は "image" フィールドに画像が来ていれば Gyazo
// にアップロードし、Note.Attachment に載せる Object を作る。フィールドが
// 無ければ (nil, nil) を返す。
//
// multipart/form-data のときしか見てはいけない。
// application/x-www-form-urlencoded にファイル部分は存在しないので
// r.FormFile を呼ぶと "request Content-Type isn't multipart/form-data" で
// 失敗する。isFormRequest はこの2つをまとめて form 判定してしまうため、
// ここでは multipart かどうかを別途見て、そうでなければ素通りする。
func imageAttachmentFromRequest(ctx context.Context, r *http.Request) (*activitystream.Object, httperror.HttpError) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		return nil, nil
	}
	file, header, err := r.FormFile("image")
	if err != nil {
		if errors.Is(err, http.ErrMissingFile) {
			return nil, nil
		}
		return nil, httperror.StatusUnprocessableEntity("bad image upload", err)
	}
	defer file.Close()

	if Config.GyazoAccessToken() == "" {
		return nil, httperror.StatusInternalServerError("image upload is not configured", nil)
	}
	result, err := uploadToGyazo(ctx, Config.GyazoAccessToken(), header.Filename, file)
	if err != nil {
		return nil, httperror.StatusInternalServerError("cannot upload the image to gyazo", err)
	}
	return &activitystream.Object{
		Type:      activitystream.ImageType,
		URL:       result.URL,
		MediaType: result.MediaType,
	}, nil
}

func appendUnique(xs []string, v string) []string {
	for _, x := range xs {
		if x == v {
			return xs
		}
	}
	return append(xs, v)
}
