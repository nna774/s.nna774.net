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

var region = "ap-northeast-1" //
var tableName = os.Getenv("DYNAMODB_TABLE_NAME")
var kvTableName = os.Getenv("DYNAMODB_KV_TABLE_NAME")

// dynamodbEndpoint が空でない場合はそこを向く。dynamodb-local で
// ローカル検証するときに使う。
var dynamodbEndpoint = os.Getenv("DYNAMODB_ENDPOINT")

var Config *config.Config
var signer *httpsigclient.Signer
var client datastore.Client
var authenticator *auth.Authenticator

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

	signer, err = httpsigclient.NewSigner(Config.PrivateKey(), Config.PublicKey(), mainKeyURI())
	if err != nil {
		return err
	}

	client, err = datastore.NewClient(ctx, region, tableName, kvTableName, dynamodbEndpoint)
	if err != nil {
		return err
	}

	// Cookie の Secure 属性は HTTPS でないと送られない。localhost での
	// 開発では外す。
	authenticator, err = auth.New(Config.APIToken(), Config.SessionSecret(), !config.IsDevelopment())
	if err != nil {
		return err
	}
	return nil
}

const (
	outboxKey = "outbox"
	statusKey = "status"
)

func inboxURI() string     { return Config.ID() + "/inbox" }
func outboxURI() string    { return Config.ID() + "/outbox" }
func followersURI() string { return Config.ID() + "/followers" }
func followingURI() string { return Config.ID() + "/following" }
func mainKeyURI() string   { return Config.ID() + "#main-key" }

func myStatusURI(id int) string { return fmt.Sprintf("%s/status/%d", Config.ID(), id) }

// newActivityID は自分が送る Activity の id を作る。リモートは id で
// 重複を排除するため一意でなければならない。秒精度だと同一秒内の
// Accept が衝突するのでナノ秒を使う。
func newActivityID(kind string) string {
	return fmt.Sprintf("%s/%s/%d", Config.Origin, kind, time.Now().UnixNano())
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
	if !(res == Config.Username || res == Config.ID() || slices.Contains(Config.AliasUsernames, res)) {
		return httperror.StatusNotFound(fmt.Sprintf("resource %v not found", resource[0]), nil)
	}
	// subject には常に正規のハンドルを返す。旧ハンドルで引かれた場合も
	// これで正規の方へ誘導される。
	resp := webfinger.NewWebFingerUserResource(Config.Username, Config.ID(), Config.Origin)
	// JRD の Content-Type は application/jrd+json である。
	w.Header().Set("Content-Type", "application/jrd+json; charset=utf-8")
	return respondJSONWithoutActivityType(w, http.StatusOK, resp)
}

// profileFields は config の fields を actor の attachment に直す。
func profileFields() activitystream.Objects {
	if len(Config.Fields) == 0 {
		return nil
	}
	fields := make(activitystream.Objects, 0, len(Config.Fields))
	for _, f := range Config.Fields {
		fields = append(fields, activitystream.NewPropertyValue(f.Name, f.Value))
	}
	return fields
}

func jsonUserHander(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	resp := activitystream.NewUserResource(
		Config.ID(), Config.Name, Config.IconURI, Config.IconMediaType(), Config.LocalPart(), inboxURI(), outboxURI(), followersURI(), followingURI(), Config.Summary, mainKeyURI(), Config.PublicKey(), profileFields())
	return respondAsJSON(w, http.StatusOK, resp)
}

// wantsActivityJSON は ActivityPub のクライアントかブラウザかを判定する。
// 以前は Accept に "json" が含まれるかという雑な判定だった。
func wantsActivityJSON(r *http.Request) bool {
	accept := strings.ToLower(r.Header.Get("Accept"))
	for _, t := range []string{"application/activity+json", "application/ld+json", "application/json"} {
		if strings.Contains(accept, t) {
			return true
		}
	}
	return false
}

func userHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	// 同じ URI が Accept によって JSON と HTML を返すので、キャッシュに
	// 混ざらないよう Vary を付ける。
	w.Header().Set("Vary", "Accept")
	if wantsActivityJSON(r) {
		return jsonUserHander(w, r)
	}
	return htmlUserHandler(w, r)
}

func outboxHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	itemsCnt, err := client.Top(r.Context(), outboxKey)
	if err != nil && !errors.Is(err, datastore.ErrNotFound) { // ErrNotFound の時は1つもitemが無い。
		return httperror.StatusInternalServerError("cannot fetch from datastore", err)
	}
	// first は最新から始まるページ、last は最古から始まるページ。
	outbox := activitystream.NewOrderedCollection(outboxURI(), itemsCnt,
		outboxURI()+"/page", outboxURI()+"/page?since_id=0")
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

	items, err := client.TakeObject(ctx, outboxKey, base, outboxPerPage, order)
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
		absoluteURI(r), outboxURI(), "", "", items)

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
			page.Next = fmt.Sprintf("%s/page?until_id=%d", outboxURI(), oldest)
		}
		page.Prev = fmt.Sprintf("%s/page?since_id=%d", outboxURI(), newest)
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

func statusHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	w.Header().Set("Vary", "Accept")
	id, herr := statusIDFromRequest(r)
	if herr != nil {
		return herr
	}
	status, err := client.GetObject(r.Context(), statusKey, id)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return httperror.StatusNotFound("no such status", err)
		}
		return httperror.StatusInternalServerError("cannot load the status", err)
	}
	if !wantsActivityJSON(r) {
		return htmlStatusHandler(w, r, id, status)
	}
	return respondAsJSON(w, http.StatusOK, status)
}

func indexHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	if r.URL.Path != "/" {
		return httperror.StatusNotFound("", nil)
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("hello"))
	log.Printf("called with %+v", r)
	return nil
}

func noteToCreate(note *activitystream.Object) *activitystream.Object {
	createID := note.ID + "/activity"
	return activitystream.NewCreate(createID, note.AttributedTo.ID(), note.To, note.Cc, note)
}

func saveStatus(ctx context.Context, id int, noteLike *activitystream.Object) error {
	return client.Put(ctx, statusKey, id, noteLike)
}

func saveToOutbox(ctx context.Context, id int, create *activitystream.Object) error {
	if err := client.Put(ctx, outboxKey, id, create); err != nil {
		return err
	}
	_, err := client.Inc(ctx, outboxKey)
	return err
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

func main() {
	if err := setup(context.Background()); err != nil {
		log.Fatalf("startup failed: %v", err)
	}

	r := httprouter.New()

	// --- 公開 (認証を掛けてはならない) ---------------------------------
	// ここに認証を掛けると連合が黙って壊れる。inbox は HTTP Signature
	// という別系統で守られている。
	pub(r, http.MethodGet, "/", indexHandler)
	pub(r, http.MethodGet, "/u/:user", userHandler)
	pub(r, http.MethodPost, "/u/:user/inbox", postInboxHandler)
	pub(r, http.MethodGet, "/u/:user/outbox", outboxHandler)
	pub(r, http.MethodGet, "/u/:user/outbox/page", outboxPageHandler)
	pub(r, http.MethodGet, "/u/:user/status/:id", statusHandler)
	pub(r, http.MethodGet, "/u/:user/followers", collectionHandler(datastore.KVFollowers, followersURI, "フォロワー"))
	pub(r, http.MethodGet, "/u/:user/following", collectionHandler(datastore.KVFollowing, followingURI, "フォロー中"))

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
	// 他インスタンスのリモートフォローボタンから辿られる。webfinger の
	// subscribe テンプレートで広告しているので実装が無いと 404 になる。
	priv(r, http.MethodGet, "/authorize_interaction", false, authorizeInteractionHandler)
	priv(r, http.MethodPost, "/u/:user/statuses", true, postStatusHandler)
	// HTML の form は DELETE を送れないので、フォーム用に POST 版も用意する。
	priv(r, http.MethodPost, "/u/:user/statuses/:id/delete", true, deleteStatusHandler)
	priv(r, http.MethodDelete, "/u/:user/status/:id", true, deleteStatusHandler)
	priv(r, http.MethodPost, "/u/:user/following", true, followRequestHandler)
	priv(r, http.MethodDelete, "/u/:user/following", true, unfollowRequestHandler)

	if config.IsDevelopment() {
		http.ListenAndServe("localhost:8080", r)
	} else {
		algnhsa.ListenAndServe(r, &algnhsa.Options{RequestType: algnhsa.RequestTypeAPIGatewayV1})
	}
}
