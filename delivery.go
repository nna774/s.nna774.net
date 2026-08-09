package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/nna774/s.nna774.net/activitystream"
	"github.com/nna774/s.nna774.net/config"
)

func logf(format string, args ...interface{}) { log.Printf(format, args...) }

const timelineKey = "timeline"

// appendToTimeline は受信した Activity を新しい順に読めるよう連番で保存する。
func appendToTimeline(ctx context.Context, in *activitystream.Object) error {
	_, err := appendToTimelineWithID(ctx, in)
	return err
}

// appendToTimelineWithID は appendToTimeline と同じだが、割り当てた連番も
// 返す。自分のブーストのように、あとで Undo のために取り消す先を覚えて
// おきたい場合に使う。
func appendToTimelineWithID(ctx context.Context, in *activitystream.Object) (int, error) {
	id, err := client.Inc(ctx, timelineKey)
	if err != nil {
		return 0, err
	}
	if err := client.Put(ctx, timelineKey, id, in); err != nil {
		return 0, err
	}
	return id, nil
}

// sendToInbox は actor の鍵で署名して1つの inbox に Activity を送る。
func sendToInbox(ctx context.Context, actor *config.ActorConfig, to string, object *activitystream.Object) error {
	buf := &bytes.Buffer{}
	if err := json.NewEncoder(buf).Encode(object); err != nil {
		return err
	}
	resp, err := signerFor(actor).RequestWithSign(ctx, http.MethodPost, to, buf.Bytes())
	if err != nil {
		return fmt.Errorf("post to inbox %v failed: %w", to, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("inbox %v returned %v: %s", to, resp.Status, body)
	}
	logf("delivered %v to %v (%v)", object.Type, to, resp.Status)
	return nil
}

// sendToActor は actor を引いてその inbox に送る。
func sendToActor(ctx context.Context, actor *config.ActorConfig, actorURI string, object *activitystream.Object) error {
	remote, err := fetchActor(ctx, actor, actorURI)
	if err != nil {
		return err
	}
	inbox := remote.InboxURI()
	if inbox == "" {
		return fmt.Errorf("actor %v advertises no inbox", actorURI)
	}
	return sendToInbox(ctx, actor, inbox, object)
}

// sendAccept は受け取った Activity をそのまま object に入れた Accept を返す。
// inbox が既に分かっている場合は actor の再取得を省ける。
func sendAccept(ctx context.Context, actor *config.ActorConfig, in *activitystream.Object, inbox string) error {
	accept := activitystream.NewAccept(in, actor.ID(), newActivityID("accept"))
	if inbox != "" {
		return sendToInbox(ctx, actor, inbox, accept)
	}
	return sendToActor(ctx, actor, in.Actor.ID(), accept)
}

// DeliveryError は配信の部分的な失敗をまとめる。1つの宛先が落ちても
// 他への配信は続けたいが、呼び出し側には知らせる必要がある。
type DeliveryError struct {
	Failures map[string]error
}

func (e *DeliveryError) Error() string {
	return fmt.Sprintf("delivery failed for %d inbox(es): %v", len(e.Failures), e.Failures)
}

// deliver は複数の inbox に同じ Activity を配信する。
//
// 1人用インスタンスで宛先が少ないため同期配信する。Lambda の 30 秒に
// 収まらなくなったら、この関数を SQS 経由の非同期配信に差し替える。
func deliver(ctx context.Context, actor *config.ActorConfig, inboxes []string, object *activitystream.Object) error {
	failures := map[string]error{}
	for _, inbox := range inboxes {
		if err := sendToInbox(ctx, actor, inbox, object); err != nil {
			logf("delivery to %v failed: %v", inbox, err)
			failures[inbox] = err
		}
	}
	if len(failures) == 0 {
		return nil
	}
	return &DeliveryError{Failures: failures}
}
