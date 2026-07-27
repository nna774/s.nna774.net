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

func init() {
	ctx := context.Background()

	cnf, err := config.LoadConfig(ctx, configFile, region)
	if err != nil {
		panic(err)
	}
	Config = cnf

	signer, err = httpsigclient.NewSigner(Config.PrivateKey(), Config.PublicKey(), mainKeyURI())
	if err != nil {
		panic(err)
	}

	client, err = datastore.NewClient(ctx, region, tableName, kvTableName, dynamodbEndpoint)
	if err != nil {
		panic(err)
	}
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
	buf := &bytes.Buffer{}
	e := json.NewEncoder(buf)
	e.SetIndent("", "  ")
	err := e.Encode(body)
	if err != nil {
		return httperror.StatusInternalServerError("json encode failed", err)
	}
	w.Header().Set("Content-Type", activitystream.ContentType)
	w.WriteHeader(status)
	io.Copy(w, buf)
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
	log.Printf("resource: %+v", resource)
	res := resource[0]
	res = strings.TrimPrefix(res, "acct:")
	if !(res == Config.Username || slices.Contains(Config.AliasUsernames, res)) {
		return httperror.StatusNotFound(fmt.Sprintf("resource %v not found", resource[0]), nil)
	}
	resp := webfinger.NewWebFingerUserResource(Config.Username, Config.ID())
	return respondAsJSON(w, http.StatusOK, resp)
}

func jsonUserHander(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	resp := activitystream.NewUserResource(
		Config.ID(), Config.Name, Config.IconURI, Config.IconMediaType(), Config.LocalPart(), inboxURI(), outboxURI(), followersURI(), followingURI(), Config.Summary, mainKeyURI(), Config.PublicKey())
	return respondAsJSON(w, http.StatusOK, resp)
}

func userHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	if strings.Contains(r.Header.Get("accept"), "json") {
		return jsonUserHander(w, r)
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("hello! " + r.URL.Path))
	return nil
}

func outboxHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	itemsCnt, err := client.Top(r.Context(), outboxKey)
	if err != nil && !errors.Is(err, datastore.ErrNotFound) { // ErrNotFound の時は1つもitemが無い。
		return httperror.StatusInternalServerError("cannot fetch from datastore", err)
	}
	outbox := activitystream.NewOrderedCollection(outboxURI(), itemsCnt, outboxURI()+"/page", outboxURI()+"/page?since=0")
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

func outboxPageHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	defaultPerPage := 20
	sinceID, err := flattenParam(r, "since_id")
	if err != nil {
		return httperror.StatusUnprocessableEntity("", err)
	}
	untilID, err := flattenParam(r, "until_id")
	if err != nil {
		return httperror.StatusUnprocessableEntity("", err)
	}

	page := (*activitystream.Object)(nil)
	if sinceID == "" && untilID == "" {
		items, err := client.TakeObject(r.Context(), outboxKey, datastore.Inf, defaultPerPage, datastore.Desc)
		if err != nil {
			return httperror.StatusInternalServerError("failed", err)
		}
		next := "next"
		prev := "prev"
		page = activitystream.NewOrderedCollectionPage(r.URL.String(), outboxURI(), next, prev, items)
	}

	return respondAsJSON(w, http.StatusOK, page)
}

func statusHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	params := httprouter.ParamsFromContext(r.Context())
	idStr := params.ByName("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return httperror.StatusUnprocessableEntity(fmt.Sprintf("bad status id: %v", idStr), err)
	}
	status, err := client.GetObject(r.Context(), statusKey, id)
	if err != nil {
		return httperror.StatusUnprocessableEntity("fetch failed", err)
	}
	return respondAsJSON(w, http.StatusOK, status)
}

func hostMetaHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<XRD xmlns="http://docs.oasis-open.org/ns/xri/xrd-1.0">
  <Link rel="lrdd" template="https://s.nna774.net/.well-known/webfinger?resource={uri}"/>
</XRD>`))
	return nil
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

func main() {
	r := httprouter.New()
	r.Handler(http.MethodGet, "/", httperror.HandleFuncWithError(indexHandler))
	r.Handler(http.MethodGet, "/u/:user", httperror.HandleFuncWithError(userHandler))
	r.Handler(http.MethodPost, "/u/:user/inbox", httperror.HandleFuncWithError(postInboxHandler))
	r.Handler(http.MethodGet, "/u/:user/outbox", httperror.HandleFuncWithError(outboxHandler))
	r.Handler(http.MethodGet, "/u/:user/outbox/page", httperror.HandleFuncWithError(outboxPageHandler))
	r.Handler(http.MethodGet, "/u/:user/status/:id", httperror.HandleFuncWithError(statusHandler))
	r.Handler(http.MethodGet, "/u/:user/followers",
		httperror.HandleFuncWithError(collectionHandler(datastore.KVFollowers, followersURI)))
	r.Handler(http.MethodGet, "/u/:user/following",
		httperror.HandleFuncWithError(collectionHandler(datastore.KVFollowing, followingURI)))

	r.Handler(http.MethodGet, "/.well-known/webfinger", httperror.HandleFuncWithError(webfingerHandler))
	r.Handler(http.MethodGet, "/.well-known/host-meta", httperror.HandleFuncWithError(hostMetaHandler))

	if config.IsDevelopment() {
		http.ListenAndServe("localhost:8080", r)
	} else {
		algnhsa.ListenAndServe(r, &algnhsa.Options{RequestType: algnhsa.RequestTypeAPIGatewayV1})
	}
}
