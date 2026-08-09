package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"

	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/julienschmidt/httprouter"
	"github.com/nna774/s.nna774.net/activitystream"
	"github.com/nna774/s.nna774.net/auth"
	"github.com/nna774/s.nna774.net/config"
	"github.com/nna774/s.nna774.net/datastore"
	"github.com/nna774/s.nna774.net/httperror"
	"github.com/nna774/s.nna774.net/httpsigclient"
	"github.com/nna774/s.nna774.net/webfinger"

	"github.com/akrylysov/algnhsa"
)

const configFile = "config.yml"

// commitHash はビルド時に -ldflags "-X main.commitHash=..." で埋め込む
// (Makefile 参照)。埋め込まずに go build した場合は "unknown" のまま。
var commitHash = "unknown"

var region = "ap-northeast-1" //
var tableName = os.Getenv("DYNAMODB_TABLE_NAME")
var kvTableName = os.Getenv("DYNAMODB_KV_TABLE_NAME")

// dynamodbEndpoint が空でない場合はそこを向く。dynamodb-local で
// ローカル検証するときに使う。
var dynamodbEndpoint = os.Getenv("DYNAMODB_ENDPOINT")

var Config *config.Config
var client datastore.Client

// signers / authenticators は Actor の localpart をキーにする。Actor ごとに
// 別の署名鍵・API トークンを持つため、パッケージ全体で1つには出来ない。
var signers map[string]*httpsigclient.Signer
var authenticators map[string]*auth.Authenticator

// setup は設定・鍵・データストア・認証を初期化する。
//
// init() ではなく main() から呼ぶ。init() に置くとテストの実行時にも走り、
// SSM と DynamoDB への接続を要求してしまう。
func setup(ctx context.Context) error {
	cnf, err := config.LoadConfig(ctx, configFile, region)
	if err != nil {
		return err
	}
	Config = cnf

	signers = map[string]*httpsigclient.Signer{}
	authenticators = map[string]*auth.Authenticator{}
	for _, a := range Config.Actors {
		s, err := httpsigclient.NewSigner(a.PrivateKey(), a.PublicKey(), mainKeyURI(a))
		if err != nil {
			return fmt.Errorf("actor %v: %w", a.Username, err)
		}
		signers[a.LocalPart()] = s

		// Cookie の Secure 属性は HTTPS でないと送られない。localhost での
		// 開発では外す。セッション鍵は全 Actor で共有する
		// (ブラウザでのログインは primary actor だけが行うため)。
		au, err := auth.New(a.APIToken(), Config.SessionSecret(), !config.IsDevelopment())
		if err != nil {
			return fmt.Errorf("actor %v: %w", a.Username, err)
		}
		authenticators[a.LocalPart()] = au
	}

	client, err = datastore.NewClient(ctx, region, tableName, kvTableName, dynamodbEndpoint)
	if err != nil {
		return err
	}
	return nil
}

const (
	outboxKey = "outbox"
	statusKey = "status"
)

// actorScoped は Actor 固有のリソース (outbox / status / followers /
// following / mylikes / myboosts / likes / announced / myboostbyid) の
// キーに localpart を前置する。primary actor (nana) も含め全 Actor を
// 対称に扱う。
//
// notification / actorkey / seen / actorinfo / cursor はローカル Actor に
// 依存しないインスタンス全体の共有リソースなので、これを通さず素の定数を
// そのまま使う。
func actorScoped(actor *config.ActorConfig, name string) string {
	return actor.LocalPart() + ":" + name
}

func inboxURI(actor *config.ActorConfig) string     { return actor.ID() + "/inbox" }
func outboxURI(actor *config.ActorConfig) string    { return actor.ID() + "/outbox" }
func followersURI(actor *config.ActorConfig) string { return actor.ID() + "/followers" }
func followingURI(actor *config.ActorConfig) string { return actor.ID() + "/following" }
func mainKeyURI(actor *config.ActorConfig) string   { return actor.ID() + "#" + Config.PublicKeyName }

func myStatusURI(actor *config.ActorConfig, id int) string {
	return fmt.Sprintf("%s/status/%d", actor.ID(), id)
}

// newActivityID は自分が送る Activity の id を作る。リモートは id で
// 重複を排除するため一意でなければならない。秒精度だと同一秒内の
// Accept が衝突するのでナノ秒を使う。
func newActivityID(kind string) string {
	return fmt.Sprintf("%s/%s/%d", Config.Origin, kind, time.Now().UnixNano())
}

// resolveActor は URL の :user を Actor の設定に解決する。
func resolveActor(r *http.Request) (*config.ActorConfig, httperror.HttpError) {
	localPart := httprouter.ParamsFromContext(r.Context()).ByName("user")
	actor, ok := Config.ActorByLocalPart(localPart)
	if !ok {
		return nil, httperror.StatusNotFound(fmt.Sprintf("no such actor %q", localPart), nil)
	}
	return actor, nil
}

// resolvePrimaryActor は :user が primary actor を指しているときだけ成功
// する。フォロー管理・いいね・ブーストなど primary actor 専用の機能で使う。
// sub actor (bot 等) はこれらの機能を持たない。
func resolvePrimaryActor(r *http.Request) (*config.ActorConfig, httperror.HttpError) {
	actor, herr := resolveActor(r)
	if herr != nil {
		return nil, herr
	}
	if !actor.Primary {
		return nil, httperror.StatusNotFound("not available for this actor", nil)
	}
	return actor, nil
}

func signerFor(actor *config.ActorConfig) *httpsigclient.Signer {
	return signers[actor.LocalPart()]
}

func authenticatorFor(actor *config.ActorConfig) *auth.Authenticator {
	return authenticators[actor.LocalPart()]
}

func primaryAuthenticator() *auth.Authenticator {
	return authenticatorFor(Config.PrimaryActor())
}

func respondAsJSON(w http.ResponseWriter, status int, body interface{}) httperror.HttpError {
	w.Header().Set("Content-Type", activitystream.ContentType)
	return respondJSONWithoutActivityType(w, status, body)
}

// respondJSONWithoutActivityType は Content-Type を呼び出し側が決める。
// webfinger は application/jrd+json、nodeinfo は profile 付きの
// application/json でなければならないため、activity+json を固定できない。
func respondJSONWithoutActivityType(w http.ResponseWriter, status int, body interface{}) httperror.HttpError {
	buf := &bytes.Buffer{}
	e := json.NewEncoder(buf)
	e.SetIndent("", "  ")
	if err := e.Encode(body); err != nil {
		return httperror.StatusInternalServerError("json encode failed", err)
	}
	w.WriteHeader(status)
	_, _ = io.Copy(w, buf)
	return nil
}

func respondText(w http.ResponseWriter, status int, msg string) {
	w.WriteHeader(status)
	w.Write([]byte(msg))
}

// findActorForWebfinger は resource (acct:user@host か actor の URI) に
// 一致する Actor を探す。
func findActorForWebfinger(res string) *config.ActorConfig {
	for _, a := range Config.Actors {
		if res == a.Username || res == a.ID() || slices.Contains(a.AliasUsernames, res) {
			return a
		}
	}
	return nil
}

func webfingerHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	param := r.URL.Query()
	if param == nil {
		return httperror.StatusUnprocessableEntity("need resource", nil)
	}

	resource := param["resource"]
	if len(resource) != 1 {
		return httperror.StatusUnprocessableEntity("resource param", nil)
	}
	res := strings.TrimPrefix(resource[0], "acct:")
	// actor の URI 自体で引かれることもある。
	actor := findActorForWebfinger(res)
	if actor == nil {
		return httperror.StatusNotFound(fmt.Sprintf("resource %v not found", resource[0]), nil)
	}
	// subject には常に正規のハンドルを返す。旧ハンドルで引かれた場合も
	// これで正規の方へ誘導される。
	resp := webfinger.NewWebFingerUserResource(actor.Username, actor.ID(), Config.Origin)
	// JRD の Content-Type は application/jrd+json である。
	w.Header().Set("Content-Type", "application/jrd+json; charset=utf-8")
	return respondJSONWithoutActivityType(w, http.StatusOK, resp)
}

// profileFields は config の fields を actor の attachment に直す。
func profileFields(actor *config.ActorConfig) activitystream.Objects {
	if len(actor.Fields) == 0 {
		return nil
	}
	fields := make(activitystream.Objects, 0, len(actor.Fields))
	for _, f := range actor.Fields {
		fields = append(fields, activitystream.NewPropertyValue(f.Name, f.Value))
	}
	return fields
}

func jsonUserHander(w http.ResponseWriter, r *http.Request, actor *config.ActorConfig) httperror.HttpError {
	resp := activitystream.NewUserResource(
		actor.ID(), actor.ActorType, actor.Name, actor.IconURI, actor.IconMediaType(), actor.LocalPart(),
		inboxURI(actor), outboxURI(actor), followersURI(actor), followingURI(actor), actor.Summary,
		mainKeyURI(actor), actor.PublicKey(), profileFields(actor))
	// followers / following と同じく、中身を出すかは favoritesHandler 側で
	// HideCollections を見て判断する。URI 自体は常に広告してよい。
	resp.Liked = favoritesURI(actor)
	return respondAsJSON(w, http.StatusOK, resp)
}

// wantsActivityJSON は ActivityPub のクライアントかブラウザかを判定する。
// 以前は Accept に "json" が含まれるかという雑な判定だった。
// URL が .json で終わる場合も JSON を要求しているとみなす。
func wantsActivityJSON(r *http.Request) bool {
	if strings.HasSuffix(r.URL.Path, ".json") {
		return true
	}
	accept := strings.ToLower(r.Header.Get("Accept"))
	for _, t := range []string{"application/activity+json", "application/ld+json", "application/json"} {
		if strings.Contains(accept, t) {
			return true
		}
	}
	return false
}

func userHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	actor, herr := resolveActor(r)
	if herr != nil {
		return herr
	}
	// 同じ URI が Accept によって JSON と HTML を返すので、キャッシュに
	// 混ざらないよう Vary を付ける。
	w.Header().Set("Vary", "Accept")
	if wantsActivityJSON(r) {
		return jsonUserHander(w, r, actor)
	}
	return htmlUserHandler(w, r, actor)
}

func outboxHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	actor, herr := resolveActor(r)
	if herr != nil {
		return herr
	}
	itemsCnt, err := client.Top(r.Context(), actorScoped(actor, outboxKey))
	if err != nil && !errors.Is(err, datastore.ErrNotFound) { // ErrNotFound の時は1つもitemが無い。
		return httperror.StatusInternalServerError("cannot fetch from datastore", err)
	}
	// first は最新から始まるページ、last は最古から始まるページ。
	outbox := activitystream.NewOrderedCollection(outboxURI(actor), itemsCnt,
		outboxURI(actor)+"/page", outboxURI(actor)+"/page?since_id=0")
	return respondAsJSON(w, http.StatusOK, outbox)
}

func flattenParam(r *http.Request, name string) (string, error) {
	p := r.URL.Query()
	value := p[name]
	if len(value) >= 2 {
		return "", fmt.Errorf("got multiple %v", name)
	}
	if len(value) == 0 {
		return "", nil
	}
	return value[0], nil
}

// intParam は数値のクエリパラメータを読む。省略時は def を返す。
func intParam(r *http.Request, name string, def int) (int, error) {
	raw, err := flattenParam(r, name)
	if err != nil {
		return 0, err
	}
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%v must be a number: %w", name, err)
	}
	return n, nil
}

const outboxPerPage = 20

// absoluteURI は API Gateway 経由だと r.URL に scheme と host が無いため、
// 設定の origin を足して絶対 URI にする。以前は r.URL.String() をそのまま
// 使っており "//s.nna774.net/..." という scheme 欠落の id を出していた。
func absoluteURI(r *http.Request) string {
	return Config.Origin + r.URL.RequestURI()
}

func outboxPageHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	ctx := r.Context()
	actor, herr := resolveActor(r)
	if herr != nil {
		return herr
	}

	// since_id は「これより新しいものを古い順に」、until_id は「これより
	// 古いものを新しい順に」。どちらも無ければ最新から新しい順に返す。
	sinceID, err := intParam(r, "since_id", -1)
	if err != nil {
		return httperror.StatusUnprocessableEntity("bad since_id", err)
	}
	untilID, err := intParam(r, "until_id", -1)
	if err != nil {
		return httperror.StatusUnprocessableEntity("bad until_id", err)
	}
	if sinceID >= 0 && untilID >= 0 {
		return httperror.StatusUnprocessableEntity("give at most one of since_id and until_id", nil)
	}

	base, order := datastore.Inf, datastore.Desc
	switch {
	case sinceID >= 0:
		base, order = sinceID, datastore.Asc
	case untilID >= 0:
		// until_id 自体は含めない。
		base, order = untilID-1, datastore.Desc
	}

	items, err := client.TakeObject(ctx, actorScoped(actor, outboxKey), base, outboxPerPage, order)
	if err != nil {
		return httperror.StatusInternalServerError("cannot read the outbox", err)
	}
	// 昇順で引いた場合も、返すのは常に新しい順に揃える。
	if order == datastore.Asc {
		for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
			items[i], items[j] = items[j], items[i]
		}
	}

	page := activitystream.NewOrderedCollectionPage(
		absoluteURI(r), outboxURI(actor), "", "", items)

	// next はより古い方へ、prev はより新しい方へ。端では省く。
	if len(items) > 0 {
		newest, herr := statusIDOf(items[0])
		if herr != nil {
			return herr
		}
		oldest, herr := statusIDOf(items[len(items)-1])
		if herr != nil {
			return herr
		}
		if len(items) == outboxPerPage {
			page.Next = fmt.Sprintf("%s/page?until_id=%d", outboxURI(actor), oldest)
		}
		page.Prev = fmt.Sprintf("%s/page?since_id=%d", outboxURI(actor), newest)
	}
	return respondAsJSON(w, http.StatusOK, page)
}

// statusIDOf は Create の中の Note の id から連番を取り出す。ページングの
// 境界を作るのに使う。
func statusIDOf(create *activitystream.Object) (int, httperror.HttpError) {
	return statusIDFromURI(create.Object.ID())
}

// statusIDFromURI は自分の投稿の URI 末尾の連番を取り出す。
func statusIDFromURI(uri string) (int, httperror.HttpError) {
	idx := strings.LastIndex(uri, "/")
	if idx < 0 {
		return 0, httperror.StatusInternalServerError("malformed status id: "+uri, nil)
	}
	n, err := strconv.Atoi(uri[idx+1:])
	if err != nil {
		return 0, httperror.StatusInternalServerError("non-numeric status id: "+uri, err)
	}
	return n, nil
}

// actorAndIDFromStatusURI は自分の投稿の URI (<actor.ID()>/status/<id>) から
// 所有する Actor と連番を取り出す。ローカルのどの Actor の投稿でもなければ
// ok は false。
func actorAndIDFromStatusURI(uri string) (actor *config.ActorConfig, id int, ok bool) {
	for _, a := range Config.Actors {
		prefix := a.ID() + "/status/"
		rest, found := strings.CutPrefix(uri, prefix)
		if !found {
			continue
		}
		n, err := strconv.Atoi(rest)
		if err != nil {
			return nil, 0, false
		}
		return a, n, true
	}
	return nil, 0, false
}

func statusHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	w.Header().Set("Vary", "Accept")
	actor, herr := resolveActor(r)
	if herr != nil {
		return herr
	}
	id, herr := statusIDFromRequest(r)
	if herr != nil {
		return herr
	}
	status, err := client.GetObject(r.Context(), actorScoped(actor, statusKey), id)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return httperror.StatusNotFound("no such status", err)
		}
		return httperror.StatusInternalServerError("cannot load the status", err)
	}
	if !wantsActivityJSON(r) {
		return htmlStatusHandler(w, r, actor, id, status)
	}
	return respondAsJSON(w, http.StatusOK, status)
}

func indexHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	if r.URL.Path != "/" {
		return httperror.StatusNotFound("", nil)
	}
	http.Redirect(w, r, "/u/"+Config.PrimaryActor().LocalPart(), http.StatusMovedPermanently)
	return nil
}

func noteToCreate(note *activitystream.Object) *activitystream.Object {
	createID := note.ID + "/activity"
	return activitystream.NewCreate(createID, note.AttributedTo.ID(), note.To, note.Cc, note)
}

func saveStatus(ctx context.Context, actor *config.ActorConfig, id int, noteLike *activitystream.Object) error {
	return client.Put(ctx, actorScoped(actor, statusKey), id, noteLike)
}

func saveToOutbox(ctx context.Context, actor *config.ActorConfig, id int, create *activitystream.Object) error {
	if err := client.Put(ctx, actorScoped(actor, outboxKey), id, create); err != nil {
		return err
	}
	_, err := client.Inc(ctx, actorScoped(actor, outboxKey))
	return err
}

// stripJSONSuffixHandler は http.Handler レベルで URL の .json 拡張子を除去する。
// ルーティング前に実行されるため、httprouter で正しくマッチングされるようになる。
// .json 拡張子がある場合は Accept ヘッダを application/activity+json に設定する。
type stripJSONSuffixHandler struct {
	handler http.Handler
}

func (h *stripJSONSuffixHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, ".json") {
		r.URL.Path = strings.TrimSuffix(r.URL.Path, ".json")
		r.Header.Set("Accept", "application/activity+json")
	}
	h.handler.ServeHTTP(w, r)
}

// pub は認証を掛けないエンドポイントを登録する。連合が依存するものは
// すべてこちらでなければならない。
func pub(r *httprouter.Router, method, path string, h httperror.HandleFuncWithError) {
	r.Handler(method, path, h)
}

// priv は認証必須のエンドポイントを登録する。mutating が true のものには
// CSRF 対策 (Sec-Fetch-Site の検証) も掛かる。
func priv(r *httprouter.Router, method, path string, mutating bool, h httperror.HandleFuncWithError) {
	r.Handler(method, path, requireAuth(mutating, h))
}

// newRouter はルーティングを組み立てる。main から切り出してあるのは、
// 経路の登録そのものをテストから確かめられるようにするため。
func newRouter() *httprouter.Router {
	r := httprouter.New()

	// --- 公開 (認証を掛けてはならない) ---------------------------------
	// ここに認証を掛けると連合が黙って壊れる。inbox は HTTP Signature
	// という別系統で守られている。
	pub(r, http.MethodGet, "/", indexHandler)
	pub(r, http.MethodGet, "/u/:user", userHandler)
	pub(r, http.MethodPost, "/u/:user/inbox", postInboxHandler)
	pub(r, http.MethodGet, "/u/:user/outbox", outboxHandler)
	pub(r, http.MethodGet, "/u/:user/outbox/page", outboxPageHandler)
	pub(r, http.MethodGet, "/u/:user/status", statusesHandler)
	pub(r, http.MethodGet, "/u/:user/status/:id", statusHandler)
	pub(r, http.MethodGet, "/u/:user/status/:id/likes", statusLikesHandler)
	pub(r, http.MethodGet, "/u/:user/status/:id/announces", statusAnnouncesHandler)
	// newActivityID が発行する Announce.ID (origin/announce/<nano>) を
	// 引けるようにする。/u/:user 配下ではない (newActivityID がそう
	// 発行しているため)。
	pub(r, http.MethodGet, "/announce/:id", announceStatusHandler)
	pub(r, http.MethodGet, "/u/:user/followers", collectionHandler(datastore.KVFollowers, "フォロワー"))
	pub(r, http.MethodGet, "/u/:user/following", collectionHandler(datastore.KVFollowing, "フォロー中"))
	pub(r, http.MethodGet, "/u/:user/favorites", favoritesHandler)

	pub(r, http.MethodGet, "/.well-known/webfinger", webfingerHandler)
	pub(r, http.MethodGet, "/.well-known/host-meta", hostMetaHandler)
	pub(r, http.MethodGet, "/.well-known/nodeinfo", nodeInfoIndexHandler)
	pub(r, http.MethodGet, "/nodeinfo/2.1", nodeInfoHandler)

	pub(r, http.MethodGet, "/login", getLoginHandler)
	pub(r, http.MethodPost, "/login", postLoginHandler)
	pub(r, http.MethodPost, "/logout", postLogoutHandler)

	// --- 私用 (認証必須) ------------------------------------------------
	priv(r, http.MethodGet, "/timeline", false, timelineHandler)
	priv(r, http.MethodGet, "/notifications", false, notificationsHandler)
	priv(r, http.MethodGet, "/remote", false, remoteProfileHandler)
	// 他インスタンスのリモートフォローボタンから辿られる。webfinger の
	// subscribe テンプレートで広告しているので実装が無いと 404 になる。
	priv(r, http.MethodGet, "/authorize_interaction", false, authorizeInteractionHandler)
	// 投稿の作成・削除は全 Actor (primary / sub actor 問わず) が持つ。
	// bot のような sub actor はこの2つだけが私用エンドポイントで、
	// following / likes / boosts は primary actor 専用。
	priv(r, http.MethodPost, "/u/:user/statuses", true, postStatusHandler)
	// HTML の form は DELETE を送れないので、フォーム用に POST 版も用意する。
	priv(r, http.MethodPost, "/u/:user/statuses/:id/delete", true, deleteStatusHandler)
	priv(r, http.MethodDelete, "/u/:user/status/:id", true, deleteStatusHandler)
	priv(r, http.MethodPost, "/u/:user/following", true, followRequestHandler)
	priv(r, http.MethodDelete, "/u/:user/following", true, unfollowRequestHandler)
	priv(r, http.MethodPost, "/u/:user/likes", true, likeRequestHandler)
	priv(r, http.MethodDelete, "/u/:user/likes", true, unlikeRequestHandler)
	priv(r, http.MethodPost, "/u/:user/boosts", true, boostRequestHandler)
	priv(r, http.MethodDelete, "/u/:user/boosts", true, unboostRequestHandler)

	return r
}

func main() {
	if err := setup(context.Background()); err != nil {
		log.Fatalf("startup failed: %v", err)
	}

	r := newRouter()
	h := &stripJSONSuffixHandler{handler: r}

	if config.IsDevelopment() {
		http.ListenAndServe("localhost:8080", h)
	} else {
		algnhsa.ListenAndServe(h, &algnhsa.Options{RequestType: algnhsa.RequestTypeAPIGatewayV1})
	}
}
