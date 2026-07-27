package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/nna774/s.nna774.net/activitystream"
	"github.com/nna774/s.nna774.net/datastore"
)

// actorKeyTTL は公開鍵キャッシュの寿命。鍵が差し替えられたときに
// 追従できなくなるので永久には持たない。
const actorKeyTTL = 24 * time.Hour

// fetchActor はリモートの actor を取得する。authorized fetch を有効に
// しているインスタンスは署名の無い GET を拒否するため、署名して取りに行く。
func fetchActor(ctx context.Context, uri string) (*activitystream.Object, error) {
	resp, err := signer.GetWithSign(ctx, uri)
	if err != nil {
		return nil, fmt.Errorf("fetching actor %v failed: %w", uri, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching actor %v returned %v", uri, resp.Status)
	}
	actor := &activitystream.Object{}
	if err := json.NewDecoder(resp.Body).Decode(actor); err != nil {
		return nil, fmt.Errorf("decoding actor %v failed: %w", uri, err)
	}
	return actor, nil
}

// publicKeyForKeyID は keyId に対応する公開鍵 PEM とその所有者 (actor の id)
// を返す。キャッシュに無ければ actor を取りに行く。
//
// 所有者を一緒に返すのが要点である。keyId の所有者と Activity の actor が
// 一致することを呼び出し側で検証しないと、自分の鍵で署名しつつ actor だけ
// 他人を名乗る偽装が通ってしまう。
func publicKeyForKeyID(ctx context.Context, keyID string) (pem string, owner string, err error) {
	if cached, err := client.GetKV(ctx, datastore.KVActorKey, keyID); err == nil {
		if cached.PublicKeyPem != "" && cached.Owner != "" {
			return cached.PublicKeyPem, cached.Owner, nil
		}
	} else if !errors.Is(err, datastore.ErrNotFound) {
		// キャッシュが引けないだけなら致命的ではない。取りに行けば良い。
		logf("actorkey cache lookup for %v failed: %v", keyID, err)
	}

	// keyId は普通 "<actor id>#main-key" の形をしている。フラグメントを
	// 落としたものを actor の URI として取りに行く。
	actorURI, err := actorURIFromKeyID(keyID)
	if err != nil {
		return "", "", err
	}
	actor, err := fetchActor(ctx, actorURI)
	if err != nil {
		return "", "", err
	}
	if actor.PublicKey == nil || actor.PublicKey.PublicKeyPem == "" {
		return "", "", fmt.Errorf("actor %v advertises no public key", actorURI)
	}
	// 取得した actor が名乗る鍵の id が、要求された keyId と一致すること。
	if actor.PublicKey.ID != "" && actor.PublicKey.ID != keyID {
		return "", "", fmt.Errorf("actor %v advertises key %v, but %v was requested", actorURI, actor.PublicKey.ID, keyID)
	}
	ownerID := actor.PublicKey.Owner
	if ownerID == "" {
		ownerID = actor.ID
	}
	if ownerID == "" {
		return "", "", fmt.Errorf("actor %v has no id", actorURI)
	}

	if err := client.PutKV(ctx, &datastore.KVItem{
		PK:           datastore.KVActorKey,
		SK:           keyID,
		PublicKeyPem: actor.PublicKey.PublicKeyPem,
		Owner:        ownerID,
		TTL:          time.Now().Add(actorKeyTTL).Unix(),
	}); err != nil {
		// キャッシュできなくても検証自体は続けられる。
		logf("caching actorkey %v failed: %v", keyID, err)
	}
	return actor.PublicKey.PublicKeyPem, ownerID, nil
}

// actorURIFromKeyID は keyId からフラグメントを落として actor の URI を作る。
func actorURIFromKeyID(keyID string) (string, error) {
	u, err := url.Parse(keyID)
	if err != nil {
		return "", fmt.Errorf("keyId %q is not a URI: %w", keyID, err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return "", fmt.Errorf("keyId %q has an unexpected scheme", keyID)
	}
	u.Fragment = ""
	return u.String(), nil
}

// sameOrigin は2つの URI が同じ scheme と host を持つかを返す。keyId の
// 所有者と actor が完全一致しない実装があるため、最後の砦としてオリジンの
// 一致を見る。
func sameOrigin(a, b string) bool {
	ua, err := url.Parse(a)
	if err != nil {
		return false
	}
	ub, err := url.Parse(b)
	if err != nil {
		return false
	}
	return ua.Scheme == ub.Scheme && ua.Host == ub.Host && ua.Host != ""
}
