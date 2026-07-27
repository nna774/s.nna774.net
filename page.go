package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/nna774/s.nna774.net/activitystream"
	"github.com/nna774/s.nna774.net/datastore"
	"github.com/nna774/s.nna774.net/httperror"
	"github.com/nna774/s.nna774.net/web"
)

// pageBase は全ページ共通の表示データ。
type pageBase struct {
	Title     string
	SiteName  string
	Origin    string
	LocalPart string
	Handle    string
	Authed    bool
	NoIndex   bool
}

func newPageBase(r *http.Request, title string) pageBase {
	authed, _ := authenticator.Authenticated(r)
	return pageBase{
		Title:     title,
		SiteName:  Config.Name,
		Origin:    Config.Origin,
		LocalPart: Config.LocalPart(),
		Handle:    "@" + Config.Username,
		Authed:    authed,
	}
}

func renderPage(w http.ResponseWriter, page string, data interface{}) httperror.HttpError {
	// テンプレートの途中でエラーになると壊れた HTML を返してしまうため、
	// 一旦バッファに書いてから流す。
	buf := &bytes.Buffer{}
	if err := web.Render(buf, page, data); err != nil {
		return httperror.StatusInternalServerError("rendering "+page+" failed", err)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
	return nil
}

// --- プロフィール -----------------------------------------------------

type profilePage struct {
	pageBase
	Name            string
	Summary         string
	IconURL         string
	StatusCount     int
	FollowerCount   int
	FollowingCount  int
	HideCollections bool
	Statuses        []*activitystream.Object
}

const profileStatusCount = 20

func htmlUserHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	ctx := r.Context()

	total, err := client.Top(ctx, outboxKey)
	if err != nil && !errors.Is(err, datastore.ErrNotFound) {
		return httperror.StatusInternalServerError("cannot count the outbox", err)
	}
	creates, err := client.TakeObject(ctx, outboxKey, datastore.Inf, profileStatusCount, datastore.Desc)
	if err != nil {
		return httperror.StatusInternalServerError("cannot read the outbox", err)
	}
	// outbox に入っているのは Create なので、中身の Note を取り出す。
	notes := make([]*activitystream.Object, 0, len(creates))
	for _, c := range creates {
		if note := c.Object.Item(); note != nil {
			notes = append(notes, note)
		}
	}

	page := profilePage{
		pageBase:        newPageBase(r, Config.Name+" ("+"@"+Config.Username+")"),
		Name:            Config.Name,
		Summary:         Config.Summary,
		IconURL:         Config.IconURI,
		StatusCount:     total,
		HideCollections: Config.HideCollections,
		Statuses:        notes,
	}
	if !Config.HideCollections {
		page.FollowerCount = countOrZero(ctx, datastore.KVFollowers)
		page.FollowingCount = countOrZero(ctx, datastore.KVFollowing)
	}
	return renderPage(w, "profile", page)
}

func countOrZero(ctx context.Context, partition string) int {
	n, err := client.CountKV(ctx, partition)
	if err != nil {
		logf("counting %v failed: %v", partition, err)
		return 0
	}
	return n
}

// --- 個別投稿 ---------------------------------------------------------

type statusPage struct {
	pageBase
	Name      string
	IconURL   string
	Content   string
	Published string
	ObjectURI string
	InReplyTo string
	Excerpt   string
	StatusID  int
}

func htmlStatusHandler(w http.ResponseWriter, r *http.Request, id int, note *activitystream.Object) httperror.HttpError {
	page := statusPage{
		pageBase:  newPageBase(r, Config.Name+": "+excerpt(note.Content, 40)),
		Name:      Config.Name,
		IconURL:   Config.IconURI,
		Content:   note.Content,
		Published: note.Published,
		ObjectURI: note.ID,
		InReplyTo: note.InReplyTo.ID(),
		Excerpt:   excerpt(note.Content, 140),
		StatusID:  id,
	}
	return renderPage(w, "status", page)
}

// excerpt は og:description 用に本文を短く切る。タグは落とす。
func excerpt(html string, max int) string {
	text := strings.TrimSpace(stripTags(html))
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max]) + "…"
}

func stripTags(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch {
		case r == '<':
			depth++
		case r == '>' && depth > 0:
			depth--
		case depth == 0:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// --- タイムライン -----------------------------------------------------

type timelineItem struct {
	AuthorName string
	AuthorURI  string
	IconURL    string
	Content    string
	Published  string
	ObjectURI  string
	InReplyTo  string
}

type timelinePage struct {
	pageBase
	Items          []timelineItem
	InReplyTo      string
	MentionPrefill string
	// FollowPrefill は authorize_interaction から来たときに埋まる。
	FollowPrefill string
}

const timelinePageSize = 40

func timelineHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	ctx := r.Context()

	activities, err := client.TakeObject(ctx, timelineKey, datastore.Inf, timelinePageSize, datastore.Desc)
	if err != nil && !errors.Is(err, datastore.ErrNotFound) {
		return httperror.StatusInternalServerError("cannot read the timeline", err)
	}

	items := make([]timelineItem, 0, len(activities))
	for _, act := range activities {
		note := act.Object.Item()
		if note == nil {
			continue
		}
		items = append(items, timelineItem{
			AuthorName: authorName(ctx, act.Actor.ID()),
			AuthorURI:  act.Actor.ID(),
			IconURL:    cachedIconURL(ctx, act.Actor.ID()),
			Content:    note.Content,
			Published:  note.Published,
			ObjectURI:  note.ID,
			InReplyTo:  note.InReplyTo.ID(),
		})
	}

	page := timelinePage{
		pageBase:  newPageBase(r, "タイムライン"),
		Items:     items,
		InReplyTo: r.URL.Query().Get("in_reply_to"),
	}
	// 返信リンクから来たときは mention 先を埋めておく。
	page.MentionPrefill = r.URL.Query().Get("mentions")
	page.FollowPrefill = r.URL.Query().Get("follow")
	// 認証必須のページなので検索避けする。
	page.NoIndex = true
	return renderPage(w, "timeline", page)
}

// authorName / cachedIconURL は KV に持っている表示名とアイコンを引く。
// 表示のたびにリモートへ actor を取りに行かないための措置。
func authorName(ctx context.Context, actorURI string) string {
	if it := lookupKnownActor(ctx, actorURI); it != nil && it.Name != "" {
		return it.Name
	}
	return actorURI
}

func cachedIconURL(ctx context.Context, actorURI string) string {
	if it := lookupKnownActor(ctx, actorURI); it != nil {
		return it.IconURL
	}
	return ""
}

func lookupKnownActor(ctx context.Context, actorURI string) *datastore.KVItem {
	if actorURI == "" {
		return nil
	}
	for _, partition := range []string{datastore.KVFollowing, datastore.KVFollowers} {
		if it, err := client.GetKV(ctx, partition, actorURI); err == nil {
			return it
		}
	}
	return nil
}

// --- ログイン ---------------------------------------------------------

type loginPage struct {
	pageBase
	Next   string
	Failed bool
}

func getLoginHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	if ok, _ := authenticator.Authenticated(r); ok {
		http.Redirect(w, r, "/timeline", http.StatusSeeOther)
		return nil
	}
	next := r.URL.Query().Get("next")
	if !isSafeRedirect(next) {
		next = "/timeline"
	}
	page := loginPage{
		pageBase: newPageBase(r, "ログイン"),
		Next:     next,
		Failed:   r.URL.Query().Get("failed") != "",
	}
	page.NoIndex = true
	return renderPage(w, "login", page)
}
