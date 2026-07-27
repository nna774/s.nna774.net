package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/nna774/s.nna774.net/activitystream"
	"github.com/nna774/s.nna774.net/datastore"
	"github.com/nna774/s.nna774.net/httperror"
	"github.com/nna774/s.nna774.net/httpsigclient"
)

// maxInboxBody は inbox で受け取る本文の上限。無制限に読むと大きな本文を
// 投げ込まれてメモリと実行時間を食われる。
const maxInboxBody = 1 << 20 // 1MiB

// seenTTL は重複配信・リプレイ排除の記録の寿命。
const seenTTL = 7 * 24 * time.Hour

func postInboxHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	ctx := r.Context()

	// Digest と本文を突き合わせる必要があるため、先に本文を読み切る。
	body, err := io.ReadAll(io.LimitReader(r.Body, maxInboxBody+1))
	if err != nil {
		return httperror.StatusUnprocessableEntity("cannot read body", err)
	}
	if len(body) > maxInboxBody {
		return httperror.StatusUnprocessableEntity("body too large", nil)
	}

	in := &activitystream.Object{}
	if err := json.Unmarshal(body, in); err != nil {
		return httperror.StatusUnprocessableEntity("decode failed", err)
	}
	if in.Type == "" {
		return httperror.StatusUnprocessableEntity("activity has no type", nil)
	}

	if herr := authenticateInbox(ctx, r, body, in); herr != nil {
		return herr
	}

	// 同じ Activity を二度処理しない。相手のリトライで Accept が二重に
	// 飛んだり、タイムラインに重複が載ったりするのを防ぐ。
	if in.ID != "" {
		fresh, err := markSeen(ctx, in.ID)
		if err != nil {
			logf("seen check for %v failed: %v", in.ID, err)
		} else if !fresh {
			logf("inbox: ignoring duplicate %v (%v)", in.ID, in.Type)
			respondText(w, http.StatusAccepted, "accepted\n")
			return nil
		}
	}

	return dispatchInbox(w, r, in)
}

func dispatchInbox(w http.ResponseWriter, r *http.Request, in *activitystream.Object) httperror.HttpError {
	switch in.Type {
	case activitystream.FollowType:
		return followHandler(w, r, in)
	case activitystream.UndoType:
		return undoHandler(w, r, in)
	case activitystream.AcceptType:
		return acceptHandler(w, r, in)
	case activitystream.RejectType:
		return rejectHandler(w, r, in)
	case activitystream.CreateType:
		return createHandler(w, r, in)
	}
	// 知らない type をエラーにすると送信元が延々リトライするため、
	// 受け取ったことにして捨てる。
	logf("inbox: ignoring unhandled type %q from %v", in.Type, in.Actor.ID())
	respondText(w, http.StatusAccepted, "accepted\n")
	return nil
}

// authenticateInbox は HTTP Signature を検証し、署名者が Activity の actor
// 本人であることを確かめる。
func authenticateInbox(ctx context.Context, r *http.Request, body []byte, in *activitystream.Object) httperror.HttpError {
	actorID := in.Actor.ID()
	if actorID == "" {
		return httperror.StatusUnprocessableEntity("activity has no actor", nil)
	}

	keyID, err := httpsigclient.KeyIDFromRequest(r)
	if err != nil {
		// インスタンスが消えた後に飛んでくる Delete は、鍵を引きようが
		// ないので検証できない。エラーを返すと相手が延々リトライするため、
		// 受け取ったことにして捨てられるよう呼び出し側に委ねる。
		if isTombstoneDelete(in) {
			return nil
		}
		return httperror.StatusUnauthorized("inbox requires a valid http signature", err)
	}

	pem, owner, err := publicKeyForKeyID(ctx, keyID)
	if err != nil {
		if isTombstoneDelete(in) {
			logf("inbox: dropping self-Delete from %v (key unavailable: %v)", actorID, err)
			return nil
		}
		return httperror.StatusUnauthorized("cannot resolve the signing key", err)
	}

	// 鍵の所有者が Activity の actor 本人であること。これを見ないと、
	// 自分の正しい鍵で署名しつつ actor だけ他人を名乗れてしまう。
	if owner != actorID && !sameOrigin(owner, actorID) {
		return httperror.StatusUnauthorized(
			fmt.Sprintf("key %v belongs to %v, but the activity claims to be from %v", keyID, owner, actorID), nil)
	}

	if err := httpsigclient.Verify(r, body, pem); err != nil {
		if isTombstoneDelete(in) {
			logf("inbox: dropping self-Delete from %v (%v)", actorID, err)
			return nil
		}
		return httperror.StatusUnauthorized("http signature is not valid", err)
	}
	return nil
}

// isTombstoneDelete は「actor 自身の削除」かを返す。送信元インスタンスが
// 消えている場合が多く、公開鍵を引けないため署名検証ができない。
func isTombstoneDelete(in *activitystream.Object) bool {
	return in.Type == activitystream.DeleteType && in.Actor.ID() != "" && in.Actor.ID() == in.Object.ID()
}

// markSeen は activity id を記録し、初めて見たものなら true を返す。
func markSeen(ctx context.Context, activityID string) (bool, error) {
	if _, err := client.GetKV(ctx, datastore.KVSeen, activityID); err == nil {
		return false, nil
	} else if !errors.Is(err, datastore.ErrNotFound) {
		return true, err
	}
	return true, client.PutKV(ctx, &datastore.KVItem{
		PK:  datastore.KVSeen,
		SK:  activityID,
		At:  nowRFC3339(),
		TTL: time.Now().Add(seenTTL).Unix(),
	})
}

func followHandler(w http.ResponseWriter, r *http.Request, in *activitystream.Object) httperror.HttpError {
	ctx := r.Context()
	actorID := in.Actor.ID()

	// object は文字列 URI で来ることも埋め込みオブジェクトで来ることも
	// ある。Ref.ID() がどちらでも id を返す。
	if in.Object.ID() != Config.ID() {
		return httperror.StatusUnprocessableEntity(
			fmt.Sprintf("Follow targets %v, not this actor", in.Object.ID()), nil)
	}

	actor, err := fetchActor(ctx, actorID)
	if err != nil {
		return httperror.StatusInternalServerError("cannot fetch the follower's actor", err)
	}
	if actor.InboxURI() == "" {
		return httperror.StatusUnprocessableEntity(fmt.Sprintf("actor %v advertises no inbox", actorID), nil)
	}

	if !Config.AutoAcceptFollow {
		// 保留する。手動で Accept するまでフォロワーには数えない。
		if err := saveFollower(ctx, datastore.KVFollowers, actor, in.ID, datastore.FollowStatePending); err != nil {
			return httperror.StatusInternalServerError("cannot record the follow request", err)
		}
		logf("follow request from %v is pending", actorID)
		respondText(w, http.StatusAccepted, "accepted\n")
		return nil
	}

	// Accept を送る前に永続化する。逆順だと、Accept は届いたのに
	// フォロワーとして記録されていない状態が起こり得る。
	if err := saveFollower(ctx, datastore.KVFollowers, actor, in.ID, datastore.FollowStateAccepted); err != nil {
		return httperror.StatusInternalServerError("cannot record the follower", err)
	}

	// Lambda はレスポンスを返した時点で実行環境を凍結するため、goroutine に
	// 投げた Accept は大抵送信されないまま殺される。同期的に送る。
	if err := sendAccept(ctx, in, actor.InboxURI()); err != nil {
		return httperror.StatusInternalServerError("failed to send Accept", err)
	}
	logf("accepted follow from %v", actorID)
	respondText(w, http.StatusAccepted, "accepted\n")
	return nil
}

// undoHandler は Undo(Follow) を処理する。Like や Announce の Undo は
// 内側の type で分岐する。
func undoHandler(w http.ResponseWriter, r *http.Request, in *activitystream.Object) httperror.HttpError {
	ctx := r.Context()
	actorID := in.Actor.ID()
	inner := in.Object.Item()

	switch {
	case inner != nil && inner.Type == activitystream.FollowType:
		// Undo の中の Follow が本当にこの actor からこの instance 宛て
		// だったものか確認する。他人のフォローを解除させない。
		if inner.Actor.ID() != "" && inner.Actor.ID() != actorID {
			return httperror.StatusUnprocessableEntity("Undo(Follow) actor mismatch", nil)
		}
		if err := client.DeleteKV(ctx, datastore.KVFollowers, actorID); err != nil {
			return httperror.StatusInternalServerError("cannot remove the follower", err)
		}
		logf("removed follower %v", actorID)
	default:
		innerType := ""
		if inner != nil {
			innerType = inner.Type
		}
		logf("inbox: ignoring Undo of %q from %v", innerType, actorID)
	}
	respondText(w, http.StatusAccepted, "accepted\n")
	return nil
}

// acceptHandler は自分が送った Follow が受理されたときに呼ばれる。
func acceptHandler(w http.ResponseWriter, r *http.Request, in *activitystream.Object) httperror.HttpError {
	ctx := r.Context()
	actorID := in.Actor.ID()

	item, err := client.GetKV(ctx, datastore.KVFollowing, actorID)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			logf("inbox: Accept from %v, but we never followed them", actorID)
			respondText(w, http.StatusAccepted, "accepted\n")
			return nil
		}
		return httperror.StatusInternalServerError("cannot look up the follow", err)
	}
	item.State = datastore.FollowStateAccepted
	if err := client.PutKV(ctx, item); err != nil {
		return httperror.StatusInternalServerError("cannot update the follow state", err)
	}
	logf("follow of %v was accepted", actorID)
	respondText(w, http.StatusAccepted, "accepted\n")
	return nil
}

// rejectHandler は自分が送った Follow が拒否されたときに呼ばれる。
func rejectHandler(w http.ResponseWriter, r *http.Request, in *activitystream.Object) httperror.HttpError {
	ctx := r.Context()
	actorID := in.Actor.ID()
	if err := client.DeleteKV(ctx, datastore.KVFollowing, actorID); err != nil {
		return httperror.StatusInternalServerError("cannot remove the follow", err)
	}
	logf("follow of %v was rejected", actorID)
	respondText(w, http.StatusAccepted, "accepted\n")
	return nil
}

func createHandler(w http.ResponseWriter, r *http.Request, in *activitystream.Object) httperror.HttpError {
	ctx := r.Context()
	note := in.Object.Item()
	if note == nil {
		return httperror.StatusUnprocessableEntity("Create has no embedded object", nil)
	}
	if err := appendToTimeline(ctx, in); err != nil {
		return httperror.StatusInternalServerError("cannot save to the timeline", err)
	}
	respondText(w, http.StatusAccepted, "accepted\n")
	return nil
}
