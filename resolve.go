package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/nna774/s.nna774.net/httperror"
	"github.com/nna774/s.nna774.net/webfinger"
)

// resolveActorURI は "@user@host" / "user@host" / "acct:user@host" /
// actor の URL のいずれからも actor の URI を得る。
//
// ハンドルで指定できないとフォローのたびに actor の URI を調べる必要が
// あり、他インスタンスのリモートフォローボタンにも対応できない。
func resolveActorURI(ctx context.Context, input string) (string, error) {
	s := strings.TrimSpace(input)
	if s == "" {
		return "", fmt.Errorf("empty actor")
	}
	if strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://") {
		return s, nil
	}
	s = strings.TrimPrefix(s, "acct:")
	s = strings.TrimPrefix(s, "@")
	user, host, ok := strings.Cut(s, "@")
	if !ok || user == "" || host == "" {
		return "", fmt.Errorf("%q is neither a URL nor a user@host handle", input)
	}
	return webfingerLookup(ctx, user, host)
}

// webfingerLookup は user@host から actor の URI を引く。
func webfingerLookup(ctx context.Context, user, host string) (string, error) {
	endpoint := fmt.Sprintf("https://%s/.well-known/webfinger?resource=%s",
		host, url.QueryEscape("acct:"+user+"@"+host))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("accept", "application/jrd+json, application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("webfinger lookup of %v@%v failed: %w", user, host, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("webfinger lookup of %v@%v returned %v", user, host, resp.Status)
	}

	var jrd webfinger.WebFingerUserResource
	if err := json.NewDecoder(resp.Body).Decode(&jrd); err != nil {
		return "", fmt.Errorf("decoding webfinger response from %v failed: %w", host, err)
	}
	for _, l := range jrd.Links {
		// rel=self かつ ActivityPub の type を持つものが actor。
		if l.Rel == "self" && strings.Contains(l.Type, "activity+json") && l.Href != "" {
			return l.Href, nil
		}
	}
	return "", fmt.Errorf("%v@%v has no ActivityPub actor link", user, host)
}

// authorizeInteractionHandler は他インスタンスのリモートフォローボタンから
// 辿られる。webfinger の subscribe テンプレートで広告している。
//
// 相手のハンドルを actor の URI に解決し、フォローフォームを埋めた
// タイムラインに送る。実際にフォローするかは自分で押して決める。
func authorizeInteractionHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	raw := strings.TrimSpace(r.URL.Query().Get("uri"))
	if raw == "" {
		return httperror.StatusUnprocessableEntity("uri query parameter is required", nil)
	}
	actorURI, err := resolveActorURI(r.Context(), raw)
	if err != nil {
		return httperror.StatusUnprocessableEntity("cannot resolve that handle", err)
	}
	http.Redirect(w, r, "/timeline?follow="+url.QueryEscape(actorURI), http.StatusSeeOther)
	return nil
}
