package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/nna774/s.nna774.net/activitystream"
	"github.com/nna774/s.nna774.net/config"
	"github.com/nna774/s.nna774.net/datastore"
	"github.com/nna774/s.nna774.net/httperror"
	"github.com/nna774/s.nna774.net/web"
)

// pageBase は全ページ共通の表示データ。SiteName / Handle 等のサイト全体の
// 識別情報は常に primary actor (nana) のもの。どのアクターのプロフィール・
// 投稿ページを見ていてもナビゲーションのブランドは変わらない。ログイン状態も
// primary actor のセッションを見る (ブラウザでのログインは primary actor
// だけが行うため)。
type pageBase struct {
	Title     string
	SiteName  string
	Origin    string
	LocalPart string
	Handle    string
	Authed    bool
	NoIndex   bool
	// UnreadCount はヘッダに出す未読通知の数。数えるのに DynamoDB を引く
	// ので、newPageBase では設定せず、出したいページが自分で入れる。
	UnreadCount int
	// CommitURL はフッターの「ActivityPub」リンク先。デプロイされている
	// commit を確認できるようにするためのもので、今のところ timeline
	// でしか入れない。空なら通常のリンクなしテキストのまま。
	CommitURL string
}

func newPageBase(r *http.Request, title string) pageBase {
	primary := Config.PrimaryActor()
	authed, _ := primaryAuthenticator().Authenticated(r)
	return pageBase{
		Title:     title,
		SiteName:  primary.Name,
		Origin:    Config.Origin,
		LocalPart: primary.LocalPart(),
		Handle:    "@" + primary.Username,
		Authed:    authed,
	}
}

// commitURL はデプロイされている commit の GitHub 上のリンク。ldflags で
// commitHash を埋め込まずに go build した場合は空を返す。
func commitURL() string {
	if commitHash == "" || commitHash == "unknown" {
		return ""
	}
	return "https://github.com/nna774/s.nna774.net/commit/" + commitHash
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
	FavoriteCount   int
	HideCollections bool
	Fields          activitystream.Objects
	Statuses        []profileStatusItem
	// HasMore はプロフィールに出し切れなかった投稿があることを示す。
	// 立っているときだけ投稿一覧へのリンクを出す。自分の Note の件数だけで
	// 判定しており、ブーストの分は数えていない (myRecentBoosts が全件では
	// なく直近 profileBoostScanLimit 件までしか遡らないため、正確な件数を
	// 出せない)。
	HasMore bool
}

// profileStatusItem はプロフィールに並べる1件分。自分の投稿とブーストを
// 混ぜて表示時刻順に並べるため、生の Note ではなくこちらに落とす。
type profileStatusItem struct {
	Content     string
	Attachments []attachmentItem
	Published   string
	URL         string
	InReplyTo   string
	// Boosted / AuthorName / AuthorURI / AnnounceURI は自分がブーストした
	// 他人の投稿のときだけ入る。AnnounceURI は日時のリンク先に使う。
	Boosted     bool
	AuthorName  string
	AuthorURI   string
	AnnounceURI string
	sortKey     time.Time
}

const profileStatusCount = 20

// profileBoostScanLimit は自分のブーストを拾うために timeline を遡る件数。
// timeline は全員分のログなので、自分の分だけを拾うのに走査するしかない。
// 削除やタイムラインの走査上限 (timelineScanLimit) と同じ妥協である。
const profileBoostScanLimit = 200

// myRecentBoosts は actor がブーストしたものを新しい順に最大 limit 件返す。
// timeline (受信 + ブースト) は primary actor しか持たないので、sub actor に
// ついて呼ぶと常に空になる (sub actor はブーストしない)。
func myRecentBoosts(ctx context.Context, actor *config.ActorConfig, limit int) ([]*activitystream.Object, error) {
	entries, err := client.TakeObject(ctx, timelineKey, datastore.Inf, profileBoostScanLimit, datastore.Desc)
	if err != nil && !errors.Is(err, datastore.ErrNotFound) {
		return nil, err
	}
	boosts := make([]*activitystream.Object, 0, limit)
	for _, act := range entries {
		if act.Type != activitystream.AnnounceType || act.Actor.ID() != actor.ID() {
			continue
		}
		boosts = append(boosts, act)
		if len(boosts) >= limit {
			break
		}
	}
	return boosts, nil
}

func htmlUserHandler(w http.ResponseWriter, r *http.Request, actor *config.ActorConfig) httperror.HttpError {
	ctx := r.Context()

	total, err := client.Top(ctx, actorScoped(actor, outboxKey))
	if err != nil && !errors.Is(err, datastore.ErrNotFound) {
		return httperror.StatusInternalServerError("cannot count the outbox", err)
	}
	creates, err := client.TakeObject(ctx, actorScoped(actor, outboxKey), datastore.Inf, profileStatusCount, datastore.Desc)
	if err != nil {
		return httperror.StatusInternalServerError("cannot read the outbox", err)
	}
	boosts, err := myRecentBoosts(ctx, actor, profileStatusCount)
	if err != nil {
		return httperror.StatusInternalServerError("cannot read the boosts", err)
	}

	items := make([]profileStatusItem, 0, len(creates)+len(boosts))
	// outbox に入っているのは Create なので、中身の Note を取り出す。
	for _, c := range creates {
		note := c.Object.Item()
		if note == nil {
			continue
		}
		items = append(items, profileStatusItem{
			Content:     note.Content,
			Attachments: noteAttachments(note),
			Published:   note.Published,
			URL:         note.ID,
			InReplyTo:   note.InReplyTo.ID(),
			sortKey:     publishedTime(note.Published),
		})
	}
	for _, act := range boosts {
		note := act.Object.Item()
		if note == nil {
			continue
		}
		items = append(items, profileStatusItem{
			Content:     note.Content,
			Attachments: noteAttachments(note),
			Published:   act.Published,
			URL:         note.ID,
			InReplyTo:   note.InReplyTo.ID(),
			Boosted:     true,
			AuthorName:  authorName(ctx, note.AttributedTo.ID()),
			AuthorURI:   note.AttributedTo.ID(),
			AnnounceURI: act.ID,
			sortKey:     publishedTime(act.Published),
		})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].sortKey.After(items[j].sortKey) })
	// もっと見るリンクの要否は自分の Note の件数だけで決める。投稿一覧
	// ページ自体が自分の Note しか出さないため、ブーストの分を足すと
	// 実際には出せない「その先」に誘ってしまう。
	hasMore := len(creates) >= profileStatusCount
	if len(items) > profileStatusCount {
		items = items[:profileStatusCount]
	}

	page := profilePage{
		pageBase:        newPageBase(r, actor.Name+" ("+"@"+actor.Username+")"),
		Name:            actor.Name,
		Summary:         actor.Summary,
		IconURL:         actor.IconURI,
		StatusCount:     total,
		HideCollections: actor.HideCollections,
		Fields:          profileFields(actor),
		Statuses:        items,
		HasMore:         hasMore,
	}
	if !actor.HideCollections {
		page.FollowerCount = countOrZero(ctx, actorScoped(actor, datastore.KVFollowers))
		page.FollowingCount = countOrZero(ctx, actorScoped(actor, datastore.KVFollowing))
		page.FavoriteCount = countOrZero(ctx, actorScoped(actor, datastore.KVMyLikes))
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

// --- 投稿一覧 ---------------------------------------------------------

type statusesItem struct {
	StatusID    int
	Content     string
	Attachments []attachmentItem
	Published   string
	ObjectURI   string
	InReplyTo   string
	// Boosted / AuthorName / AuthorURI / AnnounceURI は自分がブーストした
	// 他人の投稿のときだけ入る。profileStatusItem と同じ役割。
	Boosted     bool
	AuthorName  string
	AuthorURI   string
	AnnounceURI string
	// sortKey は投稿とブーストを混ぜて並べ替えるために使う。
	sortKey time.Time
}

type statusesPage struct {
	pageBase
	Statuses []statusesItem
	Filter   string
	Page     int
	PrevPage int
	NextPage int
	HasPrev  bool
	HasNext  bool
}

const statusesPerPage = 20

// 投稿一覧の絞り込み。空文字はすべて。
const (
	statusFilterAll    = ""
	statusFilterBoosts = "boosts"
	statusFilterMedia  = "media"
)

// statusesMediaScanLimit は絞り込み "media" のときに outbox を遡る上限。
// 添付ファイル付き投稿はまばらなので、1ページ分集めるのに通常の take 件
// では足りないことがある。かといって際限なく遡ると投稿数が増えたときに
// 重くなるため、固定の上限で妥協する (myRecentBoosts の
// profileBoostScanLimit と同じ考え方)。投稿がこの件数を超えて疎らに
// メディア付きだと、実際にはまだ後続ページがあるのに「無い」と出ることが
// ありうる。
const statusesMediaScanLimit = 500

// statusesRange は page ページ目を出すのに必要な取得件数と、取ったうち
// 読み飛ばす先頭の件数を返す。DynamoDB の Query は「n 件目から」を指定
// できないので、先頭から取って前のページ分を捨てるほかない。1件多く取る
// のは、次のページがあるかどうかを判定するため。
//
// 深いページほど読む量が増えるが、outbox 全体を超えることはない。削除で
// 連番に穴が空いても件数がずれないのはこちらの利点である。投稿が数千に
// なって重くなるようなら until_id 方式のカーソルに替えること。
func statusesRange(page, perPage int) (take, skip int) {
	return page*perPage + 1, (page - 1) * perPage
}

// statusesHandler は actor の投稿とブーストを古い方まで遡れる一覧を出す。
// プロフィールは最新 profileStatusCount 件しか出さないので、その先を
// 見るための入り口。filter クエリパラメータでブースト・添付ファイル付きに
// 絞り込める。ブースト・添付ファイルの絞り込みは自分のブースト記録
// (KVMyBoosts) を使うため、sub actor (ブーストしない) では常に空になる。
func statusesHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	ctx := r.Context()
	actor, herr := resolveActor(r)
	if herr != nil {
		return herr
	}

	pageNum, err := intParam(r, "page", 1)
	if err != nil {
		return httperror.StatusUnprocessableEntity("bad page", err)
	}
	if pageNum < 1 {
		return httperror.StatusUnprocessableEntity("page must be 1 or greater", nil)
	}
	filter, err := flattenParam(r, "filter")
	if err != nil {
		return httperror.StatusUnprocessableEntity("bad filter", err)
	}
	if filter != statusFilterAll && filter != statusFilterBoosts && filter != statusFilterMedia {
		return httperror.StatusUnprocessableEntity("unknown filter: "+filter, nil)
	}

	take, skip := statusesRange(pageNum, statusesPerPage)

	items := make([]statusesItem, 0, statusesPerPage*2)

	// ブーストだけを見たいときは outbox を読む必要が無い。
	if filter != statusFilterBoosts {
		outboxTake := take
		// メディア付きはまばらなので、通常の take では 1 ページ分すら
		// 集まらないことがある。上限まで多めに読んで Go 側でふるいにかける。
		if filter == statusFilterMedia {
			outboxTake = statusesMediaScanLimit
		}
		entries, err := client.TakeEntries(ctx, actorScoped(actor, outboxKey), datastore.Inf, outboxTake, datastore.Desc)
		if err != nil && !errors.Is(err, datastore.ErrNotFound) {
			return httperror.StatusInternalServerError("cannot read the outbox", err)
		}
		for _, e := range entries {
			// outbox に入っているのは Create なので、中身の Note を取り出す。
			note := e.Object.Object.Item()
			if note == nil {
				continue
			}
			attachments := noteAttachments(note)
			if filter == statusFilterMedia && len(attachments) == 0 {
				continue
			}
			items = append(items, statusesItem{
				StatusID:    e.ID,
				Content:     note.Content,
				Attachments: attachments,
				Published:   note.Published,
				ObjectURI:   note.ID,
				InReplyTo:   note.InReplyTo.ID(),
				sortKey:     publishedTime(note.Published),
			})
		}
	}

	// KVMyBoosts は自分の現在のブーストだけを持つ (誰かに配ったタイムライン
	// 全体ではない) ので、outbox のように take で絞らず全件読んでよい。
	// ページを跨いでも取りこぼしが起きないよう、常に全部を候補にする。
	if boosts, err := client.QueryKV(ctx, actorScoped(actor, datastore.KVMyBoosts)); err != nil {
		logf("loading my boosts for the status list failed: %v", err)
	} else {
		for _, b := range boosts {
			if b.TimelineID == 0 {
				continue
			}
			act, err := client.GetObject(ctx, timelineKey, b.TimelineID)
			if err != nil {
				logf("loading boost %v for the status list failed: %v", b.ActivityID, err)
				continue
			}
			note := act.Object.Item()
			if note == nil {
				continue
			}
			attachments := noteAttachments(note)
			if filter == statusFilterMedia && len(attachments) == 0 {
				continue
			}
			items = append(items, statusesItem{
				Content:     note.Content,
				Attachments: attachments,
				Published:   act.Published,
				ObjectURI:   note.ID,
				InReplyTo:   note.InReplyTo.ID(),
				Boosted:     true,
				AuthorName:  authorName(ctx, note.AttributedTo.ID()),
				AuthorURI:   note.AttributedTo.ID(),
				AnnounceURI: act.ID,
				sortKey:     publishedTime(act.Published),
			})
		}
	}

	// 新しい順に並べ直してからページを切り出す。outbox は take (または
	// statusesMediaScanLimit) で絞ってあるが、ブーストは全件候補にして
	// あるので、深いページでも取りこぼさない (timelineHandler と同じ
	// 「多めに取って捨てる」考え方)。
	sort.SliceStable(items, func(i, j int) bool { return items[i].sortKey.After(items[j].sortKey) })
	items, hasNext := paginate(items, skip, statusesPerPage)

	page := statusesPage{
		pageBase: newPageBase(r, actor.Name+" の投稿"),
		Statuses: items,
		Filter:   filter,
		Page:     pageNum,
		PrevPage: pageNum - 1,
		NextPage: pageNum + 1,
		HasPrev:  pageNum > 1,
		HasNext:  hasNext,
	}
	return renderPage(w, "statuses", page)
}

// paginate は先頭から多めに取ってきたスライスから該当ページ分を切り出し、
// 次のページがあるかどうかを返す。1件多く取っておくことで判定できる。
func paginate[T any](items []T, skip, perPage int) ([]T, bool) {
	if skip >= len(items) {
		return nil, false
	}
	items = items[skip:]
	if len(items) > perPage {
		return items[:perPage], true
	}
	return items, false
}

// --- フォロワー / フォロー中の一覧 -----------------------------------

type collectionMember struct {
	Name     string
	ActorURI string
	IconURL  string
}

type collectionPage struct {
	pageBase
	Heading string
	Members []collectionMember
}

// htmlCollectionHandler はフォロワー / フォロー中を人間向けの一覧にする。
// 中身は KV に持っている (saveFollower が名前とアイコンを控えている) ので
// リモートへ取りに行かない。
func htmlCollectionHandler(w http.ResponseWriter, r *http.Request, actor *config.ActorConfig, items []*datastore.KVItem, heading string) httperror.HttpError {
	members := make([]collectionMember, 0, len(items))
	for _, it := range items {
		members = append(members, collectionMember{
			Name:     it.Name,
			ActorURI: it.SK,
			IconURL:  it.IconURL,
		})
	}
	page := collectionPage{
		pageBase: newPageBase(r, actor.Name+" — "+heading),
		Heading:  heading,
		Members:  members,
	}
	return renderPage(w, "collection", page)
}

// --- いいね一覧 ---------------------------------------------------------

func favoritesURI(actor *config.ActorConfig) string { return actor.ID() + "/favorites" }

type favoriteItem struct {
	ObjectURI  string
	AuthorName string
	AuthorURI  string
	IconURL    string
	Content    string
	At         string
}

type favoritesPage struct {
	pageBase
	Items []favoriteItem
}

// favoritesHandler は actor がいいねした投稿の一覧。followers / following と
// 同じく、ActivityPub の liked コレクションとして JSON でも、人間向けの
// 一覧としても出す。HideCollections が立っていれば中身を出さない。sub
// actor はいいねしないため常に空になる。
func favoritesHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	ctx := r.Context()
	actor, herr := resolveActor(r)
	if herr != nil {
		return herr
	}
	// 同じ URI が Accept によって JSON と HTML を返すので、キャッシュに
	// 混ざらないよう Vary を付ける。
	w.Header().Set("Vary", "Accept")

	if actor.HideCollections {
		if wantsActivityJSON(r) {
			return respondAsJSON(w, http.StatusOK,
				activitystream.NewOrderedCollection(favoritesURI(actor), 0, "", ""))
		}
		return renderPage(w, "favorites", favoritesPage{pageBase: newPageBase(r, actor.Name+" — いいね")})
	}

	items, err := client.QueryKV(ctx, actorScoped(actor, datastore.KVMyLikes))
	if err != nil {
		return httperror.StatusInternalServerError("cannot list the favorites", err)
	}
	// KV は sk (対象投稿の URI) の辞書順でしか返らないため、いいねした順に
	// 並べ直す。
	sort.SliceStable(items, func(i, j int) bool { return items[i].At > items[j].At })

	if wantsActivityJSON(r) {
		ids := make([]string, 0, len(items))
		for _, it := range items {
			ids = append(ids, it.SK)
		}
		return respondAsJSON(w, http.StatusOK, activitystream.NewOrderedCollectionOfIDs(favoritesURI(actor), ids))
	}

	page := favoritesPage{
		pageBase: newPageBase(r, actor.Name+" — いいね"),
		Items:    make([]favoriteItem, 0, len(items)),
	}
	for _, it := range items {
		page.Items = append(page.Items, favoriteItem{
			ObjectURI:  it.SK,
			AuthorName: it.Name,
			AuthorURI:  it.TargetActor,
			IconURL:    it.IconURL,
			Content:    it.Content,
			At:         it.At,
		})
	}
	return renderPage(w, "favorites", page)
}

// --- 個別投稿 ---------------------------------------------------------

type statusPage struct {
	pageBase
	Name          string
	IconURL       string
	Content       string
	Attachments   []attachmentItem
	Published     string
	ObjectURI     string
	InReplyTo     string
	Excerpt       string
	StatusID      int
	LikeCount     int
	AnnounceCount int
}

func htmlStatusHandler(w http.ResponseWriter, r *http.Request, actor *config.ActorConfig, id int, note *activitystream.Object) httperror.HttpError {
	ctx := r.Context()
	page := statusPage{
		pageBase:      newPageBase(r, actor.Name+": "+excerpt(note.Content, 40)),
		Name:          actor.Name,
		IconURL:       actor.IconURI,
		Content:       note.Content,
		Attachments:   noteAttachments(note),
		Published:     note.Published,
		ObjectURI:     note.ID,
		InReplyTo:     note.InReplyTo.ID(),
		Excerpt:       excerpt(note.Content, 140),
		StatusID:      id,
		LikeCount:     countReactors(ctx, actorScoped(actor, datastore.KVLikes), note.ID),
		AnnounceCount: countReactors(ctx, actorScoped(actor, datastore.KVAnnounced), note.ID),
	}
	return renderPage(w, "status", page)
}

// countReactors は reactorsOf の件数だけを見たいときに使う。取得に失敗
// しても件数表示のためだけに 500 で落とすほどではないので、0 件として扱う。
func countReactors(ctx context.Context, pk, objectURI string) int {
	items, err := reactorsOf(ctx, pk, objectURI)
	if err != nil {
		logf("counting reactors (%v) of %v failed: %v", pk, objectURI, err)
		return 0
	}
	return len(items)
}

// --- いいね・ブーストした人の一覧 ---------------------------------------

type reactorItem struct {
	Name     string
	ActorURI string
	IconURL  string
	// AnnounceURI は Announce 自身の URI。/status/:id/announces でのみ
	// 使う。分かっている場合だけ元のブーストへリンクする。
	AnnounceURI string
	At          string
}

type reactorsPage struct {
	pageBase
	Heading   string
	StatusID  int
	ObjectURI string
	Items     []reactorItem
}

// statusLikesHandler は actor の投稿に付いたいいねをした人の一覧。
func statusLikesHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	return statusReactorsHandler(w, r, datastore.KVLikes, "status_likes", "いいね", false)
}

// statusAnnouncesHandler は actor の投稿をブーストした人の一覧。
func statusAnnouncesHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	return statusReactorsHandler(w, r, datastore.KVAnnounced, "status_announces", "RT", true)
}

func statusReactorsHandler(w http.ResponseWriter, r *http.Request, pk, templateName, heading string, withAnnounceLink bool) httperror.HttpError {
	ctx := r.Context()
	actor, herr := resolveActor(r)
	if herr != nil {
		return herr
	}
	id, herr := statusIDFromRequest(r)
	if herr != nil {
		return herr
	}
	status, err := client.GetObject(ctx, actorScoped(actor, statusKey), id)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return httperror.StatusNotFound("no such status", err)
		}
		return httperror.StatusInternalServerError("cannot load the status", err)
	}

	items, err := reactorsOf(ctx, actorScoped(actor, pk), status.ID)
	if err != nil {
		return httperror.StatusInternalServerError("cannot list the reactors", err)
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].At > items[j].At })

	prefix := status.ID + "#"
	page := reactorsPage{
		pageBase:  newPageBase(r, actor.Name+" — "+heading),
		Heading:   heading,
		StatusID:  id,
		ObjectURI: status.ID,
		Items:     make([]reactorItem, 0, len(items)),
	}
	for _, it := range items {
		actorURI := strings.TrimPrefix(it.SK, prefix)
		item := reactorItem{
			Name:     authorName(ctx, actorURI),
			ActorURI: actorURI,
			IconURL:  cachedIconURL(ctx, actorURI),
			At:       it.At,
		}
		if withAnnounceLink {
			item.AnnounceURI = it.ActivityID
		}
		page.Items = append(page.Items, item)
	}
	return renderPage(w, templateName, page)
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

// attachmentItem はタイムラインに出す添付メディア1件分。
type attachmentItem struct {
	URL  string
	Name string
	// Kind は "image" / "video" / "other"。テンプレート側で分岐しやすい
	// ように mediaType から先に判定しておく。
	Kind string
	// PageURL は Gyazo の画像ページ URL。サムネイル表示が小さいため、
	// クリックで原寸を見られるようにリンク先として使う。Gyazo 由来の
	// 添付でなければ空文字。
	PageURL string
}

// noteAttachments は note.Attachment のうち URL を持つものだけを表示用に
// 落とす。
func noteAttachments(note *activitystream.Object) []attachmentItem {
	if len(note.Attachment) == 0 {
		return nil
	}
	items := make([]attachmentItem, 0, len(note.Attachment))
	for _, a := range note.Attachment {
		if a.URL == "" {
			continue
		}
		item := attachmentItem{
			URL:  a.URL,
			Name: a.Name,
			Kind: attachmentKind(a.MediaType),
		}
		if pageURL, ok := gyazoImagePageURL(a.URL); ok {
			item.PageURL = pageURL
		}
		items = append(items, item)
	}
	return items
}

func attachmentKind(mediaType string) string {
	switch {
	case strings.HasPrefix(mediaType, "image/"):
		return "image"
	case strings.HasPrefix(mediaType, "video/"):
		return "video"
	default:
		return "other"
	}
}

type timelineItem struct {
	AuthorName  string
	AuthorURI   string
	IconURL     string
	Content     string
	Attachments []attachmentItem
	Published   string
	ObjectURI   string
	InReplyTo   string
	// Mine は自分の投稿かどうか。削除ボタンの出し分けに使う。
	Mine bool
	// StatusID は自分の投稿のときだけ入る。削除フォームに使う。
	StatusID int
	// BoostedByName / BoostedByURI はブーストのときだけ入る。ブーストは
	// 書いた人とブーストした人が別なので、著者とは分けて持つ。
	BoostedByName string
	BoostedByURI  string
	// AnnounceURI はブーストのときだけ入る、Announce 自身の URI。日時の
	// リンク先を元投稿ではなくブースト自体に向けるために使う (Twitter の
	// リツイート個別ページに相当)。
	AnnounceURI string
	// Liked / Boosted は自分が既にいいね・ブースト済みかどうか。ボタンの
	// 出し分けに使う。
	Liked   bool
	Boosted bool
	// sortKey は並べ替え用。published を解釈できたものはその時刻、
	// 解釈できなければゼロ値。
	sortKey time.Time
}

type timelinePage struct {
	pageBase
	Items          []timelineItem
	InReplyTo      string
	MentionPrefill string
	Page           int
	PrevPage       int
	NextPage       int
	HasPrev        bool
	HasNext        bool
}

const timelinePageSize = 40

// publishedTime は published を並べ替え可能な時刻にする。実装によって
// ISO8601 と RFC1123 のどちらも来るため両方受ける。
func publishedTime(s string) time.Time {
	for _, layout := range []string{time.RFC3339, time.RFC3339Nano, time.RFC1123, time.RFC1123Z} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// timelineHandler は primary actor 専用。sub actor (bot 等) は timeline を
// 持たない。
func timelineHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	ctx := r.Context()
	primary := Config.PrimaryActor()

	pageNum, err := intParam(r, "page", 1)
	if err != nil {
		return httperror.StatusUnprocessableEntity("bad page", err)
	}
	if pageNum < 1 {
		return httperror.StatusUnprocessableEntity("page must be 1 or greater", nil)
	}
	take, skip := statusesRange(pageNum, timelinePageSize)

	// 受信したものと自分の投稿を混ぜる。Mastodon のホームタイムラインも
	// 自分の投稿を含む。自分自身をフォローするような裏技は要らない。
	// 2つの連番は別物で、時刻でマージした後にしか順序が確定しないため、
	// 深いページを出すにはどちらの取得件数も take まで増やす必要がある
	// （statusesRange と同じ「多めに取って捨てる」方式。詳細はそちらの
	// コメントを参照）。
	received, err := client.TakeObject(ctx, timelineKey, datastore.Inf, take, datastore.Desc)
	if err != nil && !errors.Is(err, datastore.ErrNotFound) {
		return httperror.StatusInternalServerError("cannot read the timeline", err)
	}
	mine, err := client.TakeObject(ctx, actorScoped(primary, outboxKey), datastore.Inf, take, datastore.Desc)
	if err != nil && !errors.Is(err, datastore.ErrNotFound) {
		return httperror.StatusInternalServerError("cannot read the outbox", err)
	}

	reactions := loadReactionState(ctx, primary)

	items := make([]timelineItem, 0, len(received)+len(mine))
	for _, act := range append(append([]*activitystream.Object{}, received...), mine...) {
		note := act.Object.Item()
		if note == nil {
			continue
		}
		// ブーストは Activity の actor がブーストした人で、著者は中の投稿の
		// attributedTo である。並べる時刻もブーストされた時刻を使う。元の
		// 投稿の時刻で並べると、古い投稿のブーストが下に埋もれて見えない。
		actorURI := act.Actor.ID()
		published := note.Published
		boostedBy := ""
		if act.Type == activitystream.AnnounceType {
			boostedBy = actorURI
			actorURI = note.AttributedTo.ID()
			published = act.Published
		}

		isMine := actorURI == primary.ID()
		item := timelineItem{
			AuthorURI:   actorURI,
			Content:     note.Content,
			Attachments: noteAttachments(note),
			Published:   published,
			ObjectURI:   note.ID,
			InReplyTo:   note.InReplyTo.ID(),
			Mine:        isMine,
			Liked:       reactions.liked[note.ID],
			Boosted:     reactions.boosted[note.ID],
			sortKey:     publishedTime(published),
		}
		// authorName / cachedIconURL は自分の分も設定から返す。
		item.AuthorName = authorName(ctx, actorURI)
		item.IconURL = cachedIconURL(ctx, actorURI)
		if boostedBy != "" {
			item.BoostedByURI = boostedBy
			item.BoostedByName = authorName(ctx, boostedBy)
			item.AnnounceURI = act.ID
		}
		if isMine {
			// 削除フォームに使う連番は自分の投稿にしか無い。
			if n, err := statusIDOf(act); err == nil {
				item.StatusID = n
			}
		}
		items = append(items, item)
	}
	// 新しい順。published を解釈できなかったものは末尾に落ちる。
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].sortKey.After(items[j].sortKey)
	})
	items, hasNext := paginate(items, skip, timelinePageSize)

	page := timelinePage{
		pageBase:  newPageBase(r, "タイムライン"),
		Items:     items,
		InReplyTo: r.URL.Query().Get("in_reply_to"),
		Page:      pageNum,
		PrevPage:  pageNum - 1,
		NextPage:  pageNum + 1,
		HasPrev:   pageNum > 1,
		HasNext:   hasNext,
	}
	page.UnreadCount = len(unreadNotifications(ctx))
	// 返信リンクから来たときは mention 先を埋めておく。
	page.MentionPrefill = r.URL.Query().Get("mentions")
	// 認証必須のページなので検索避けする。
	page.NoIndex = true
	// どの commit がデプロイされているか footer から追えるようにする。
	page.CommitURL = commitURL()
	return renderPage(w, "timeline", page)
}

// authorName / cachedIconURL は KV に持っている表示名とアイコンを引く。
// 表示のたびにリモートへ actor を取りに行かないための措置。timeline や
// notifications は primary actor 専用のページなので、「自分」は常に
// primary actor を指す。
func authorName(ctx context.Context, actorURI string) string {
	primary := Config.PrimaryActor()
	if actorURI == primary.ID() {
		return primary.Name
	}
	if it := lookupKnownActor(ctx, actorURI); it != nil && it.Name != "" {
		return it.Name
	}
	return actorURI
}

func cachedIconURL(ctx context.Context, actorURI string) string {
	primary := Config.PrimaryActor()
	if actorURI == primary.ID() {
		return primary.IconURI
	}
	if it := lookupKnownActor(ctx, actorURI); it != nil {
		return it.IconURL
	}
	return ""
}

// lookupKnownActor は表示名を持っている項目を探す。フォロー関係を先に見る
// のは、そちらが期限切れしないため。actorinfo は TTL で消える。フォロー
// 関係は primary actor のものを見る (following / followers を読む画面は
// primary actor 専用のため)。
func lookupKnownActor(ctx context.Context, actorURI string) *datastore.KVItem {
	if actorURI == "" {
		return nil
	}
	primary := Config.PrimaryActor()
	partitions := []string{
		actorScoped(primary, datastore.KVFollowing),
		actorScoped(primary, datastore.KVFollowers),
		datastore.KVActorInfo,
	}
	for _, partition := range partitions {
		if it, err := client.GetKV(ctx, partition, actorURI); err == nil {
			return it
		}
	}
	return nil
}

// --- 通知 -------------------------------------------------------------

// 通知の種別。テンプレートで文面を分けるために使う。
const (
	kindLike     = "like"
	kindAnnounce = "announce"
	kindMention  = "mention"
	kindFollow   = "follow"
	// 取り消しと削除も出来事として並べる。元の通知は消さない。
	kindUndoLike     = "undo-like"
	kindUndoAnnounce = "undo-announce"
	kindDelete       = "delete"
)

type notificationItem struct {
	Kind      string
	ActorName string
	ActorURI  string
	IconURL   string
	// TargetURI は Like / Announce の対象になった自分の投稿、または
	// 返信先の投稿。
	TargetURI string
	// TargetExcerpt は対象の投稿の抜粋。何に対するいいねなのか分からないと
	// 通知として読めない。
	TargetExcerpt string
	// Content は返信・メンションの本文。
	Content string
	// ObjectURI は相手の投稿。返信リンクに使う。
	ObjectURI string
	Published string
	Unread    bool
}

type notificationsPage struct {
	pageBase
	Items []notificationItem
}

const notificationPageSize = 40

// notificationsHandler は primary actor 専用のページだが、通知そのものは
// 全 Actor (primary / sub 問わず) 分をまとめて表示する。通知ストリームは
// ローカル actor に依存しないインスタンス全体の共有リソースなので、bot
// 宛の Follow もここに混ざって出る。
func notificationsHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	ctx := r.Context()

	entries, err := client.TakeEntries(ctx, notificationKey, datastore.Inf, notificationPageSize, datastore.Desc)
	if err != nil && !errors.Is(err, datastore.ErrNotFound) {
		return httperror.StatusInternalServerError("cannot read the notifications", err)
	}
	unread := unreadNotifications(ctx)

	// 同じ投稿に複数のいいねが付くのが普通なので、抜粋は使い回す。
	excerpts := map[string]string{}
	items := make([]notificationItem, 0, len(entries))
	newest := 0
	for _, e := range entries {
		if e.ID > newest {
			newest = e.ID
		}
		item, ok := toNotificationItem(ctx, e.Object, excerpts)
		if !ok {
			continue
		}
		item.Unread = unread[e.ID]
		items = append(items, item)
	}

	page := notificationsPage{
		pageBase: newPageBase(r, "通知"),
		Items:    items,
	}
	page.UnreadCount = len(unread)
	page.NoIndex = true

	// 既読にするのは描画するものを決めた後。先に進めると、この描画で
	// 未読マークが付かないまま既読になる。
	if newest > 0 {
		if err := advanceReadCursor(ctx, notificationKey, newest); err != nil {
			logf("advancing the notification cursor failed: %v", err)
		}
	}
	return renderPage(w, "notifications", page)
}

// toNotificationItem は保存された Activity を表示用に落とす。扱えない
// type だったときは false を返す。
func toNotificationItem(ctx context.Context, act *activitystream.Object, excerpts map[string]string) (notificationItem, bool) {
	// Published は appendNotification が受信時刻で埋めている。連番の降順に
	// 並べるので、これが並び順と一致する唯一の時刻である。
	item := notificationItem{
		ActorURI:  act.Actor.ID(),
		Published: act.Published,
	}
	item.ActorName = authorName(ctx, item.ActorURI)
	item.IconURL = cachedIconURL(ctx, item.ActorURI)

	switch act.Type {
	case activitystream.LikeType, activitystream.AnnounceType:
		item.Kind = kindLike
		if act.Type == activitystream.AnnounceType {
			item.Kind = kindAnnounce
		}
		item.TargetURI = act.Object.ID()
		item.TargetExcerpt = myStatusExcerpt(ctx, item.TargetURI, excerpts)
	case activitystream.FollowType:
		item.Kind = kindFollow
	case activitystream.CreateType, activitystream.UpdateType:
		note := act.Object.Item()
		if note == nil {
			return notificationItem{}, false
		}
		item.Kind = kindMention
		item.Content = note.Content
		item.ObjectURI = note.ID
		item.TargetURI = note.InReplyTo.ID()
		item.TargetExcerpt = myStatusExcerpt(ctx, item.TargetURI, excerpts)
	case activitystream.UndoType:
		inner := act.Object.Item()
		if inner == nil {
			return notificationItem{}, false
		}
		switch inner.Type {
		case activitystream.LikeType:
			item.Kind = kindUndoLike
		case activitystream.AnnounceType:
			item.Kind = kindUndoAnnounce
		default:
			return notificationItem{}, false
		}
		// 取り消された対象は自分の投稿なので抜粋が引ける。何を取り消され
		// たのか分からないと通知として読めない。
		item.TargetURI = inner.Object.ID()
		item.TargetExcerpt = myStatusExcerpt(ctx, item.TargetURI, excerpts)
	case activitystream.DeleteType:
		item.Kind = kindDelete
		// 消された投稿なので本文も抜粋も引けない。URI だけ出す。
		item.ObjectURI = act.Object.ID()
	default:
		return notificationItem{}, false
	}
	return item, true
}

// myStatusExcerpt はローカル actor の投稿の URI から本文の抜粋を引く。
// ローカルの投稿でなければ空を返す。
func myStatusExcerpt(ctx context.Context, uri string, cache map[string]string) string {
	owner, id, ok := actorAndIDFromStatusURI(uri)
	if !ok {
		return ""
	}
	if s, ok := cache[uri]; ok {
		return s
	}
	cache[uri] = ""
	note, err := client.GetObject(ctx, actorScoped(owner, statusKey), id)
	if err != nil {
		// 既に消した投稿へのいいねが残っていることはある。通知自体は
		// 出したいので、抜粋だけ諦める。
		if !errors.Is(err, datastore.ErrNotFound) {
			logf("loading %v for a notification failed: %v", uri, err)
		}
		return ""
	}
	cache[uri] = excerpt(note.Content, 60)
	return cache[uri]
}

// --- ログイン ---------------------------------------------------------

type loginPage struct {
	pageBase
	Next   string
	Failed bool
}

func getLoginHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	if ok, _ := primaryAuthenticator().Authenticated(r); ok {
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
