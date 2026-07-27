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

	client, err = datastore.NewClient(ctx, region, tableName, dynamodbEndpoint)
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

func respondAsJSON(w http.ResponseWriter, status int, body interface{}) httperror.HttpError {
	buf := &bytes.Buffer{}
	e := json.NewEncoder(buf)
	e.SetIndent("", "  ")
	err := e.Encode(body)
	if err != nil {
		return httperror.StatusInternalServerError("json encode failed", err)
	}
	w.Header().Set("Content-Type", "application/activity+json")
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
		Config.ID(), Config.Name, Config.IconURI, Config.IconMediaType(), Config.LocalPart(), inboxURI(), outboxURI(), followersURI(), followingURI(), "B95 H108 S102", mainKeyURI(), Config.PublicKey())
	return respondAsJSON(w, http.StatusOK, &resp)
}

func userHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	if strings.Contains(r.Header.Get("accept"), "json") {
		return jsonUserHander(w, r)
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("hello! " + r.URL.Path))
	return nil
}

func sendToInboxFromUser(ctx context.Context, to string, object *activitystream.Object) error {
	ur, err := activitystream.FetchActorInfo(ctx, to)
	if err != nil {
		return err
	}
	return sendToInbox(ctx, ur.Inbox, object)
}

func sendToInbox(ctx context.Context, to string, object *activitystream.Object) error {
	buf := &bytes.Buffer{}
	err := json.NewEncoder(buf).Encode(object)
	if err != nil {
		return err
	}
	resp, err := signer.RequestWithSign(ctx, http.MethodPost, to, buf.Bytes())
	if err != nil {
		return fmt.Errorf("post to inbox %v failed: %w", to, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	log.Printf("send to %v: status %v, body: %s", to, resp.StatusCode, body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("inbox %v returned %v: %s", to, resp.Status, body)
	}
	return nil
}

func sendAccept(ctx context.Context, obj *activitystream.Object, act activitystream.Activity) error {
	accept := activitystream.NewAccept(act, Config.ID(), Config.Origin+"/accept/"+fmt.Sprintf("%v", time.Now().Unix()))
	return sendToInboxFromUser(ctx, act.Actor, accept)
}

func followHandler(w http.ResponseWriter, r *http.Request, in *activitystream.Object) httperror.HttpError {
	// TODO: allow only
	act, ok := in.Activity()
	if !ok {
		return httperror.StatusUnprocessableEntity("conversion failed", nil)
	}
	if act.Item.ID != Config.ID() {
		return httperror.StatusUnprocessableEntity(fmt.Sprintf("followHandler: unexpected object: %v", act.Item), nil)
	}
	// Lambda はレスポンスを返した時点で実行環境を凍結するため、goroutine に
	// 投げた Accept は大抵送信されないまま殺される。同期的に送り、失敗したら
	// エラーを返して送信元にリトライさせる。
	if err := sendAccept(r.Context(), in, act); err != nil {
		return httperror.StatusInternalServerError("failed to send Accept", err)
	}
	respondText(w, http.StatusCreated, "created\n")
	return nil
}

func createHandler(w http.ResponseWriter, r *http.Request, in *activitystream.Object) httperror.HttpError {
	ctx := r.Context()
	act, _ := in.Activity()
	id, err := client.Inc(ctx, statusKey)
	if err != nil {
		return httperror.StatusInternalServerError("inc failed", err)
	}
	if err := saveStatus(ctx, id, act.Item); err != nil {
		return httperror.StatusInternalServerError("save failed", err)
	}
	return nil
}

func postInboxHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	in := &activitystream.Object{}
	err := json.NewDecoder(r.Body).Decode(in)
	if err != nil {
		return httperror.StatusUnprocessableEntity("decode failed", err)
	}
	switch in.Type {
	case "Follow":
		return followHandler(w, r, in)
	case activitystream.CreateType:
		return createHandler(w, r, in)
	}
	respondText(w, http.StatusCreated, "created\n")
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

func sendNote(ctx context.Context, to []string, note *activitystream.Object) error {
	type r struct {
		Err error
		To  string
	}
	errs := make([]r, 0)
	for _, v := range to {
		err := sendToInbox(ctx, v, noteToCreate(note))
		if err != nil {
			errs = append(errs, r{Err: err, To: v})
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("sendNote got errors: %+v", errs)
}

func noteToCreate(note *activitystream.Object) *activitystream.Object {
	createID := note.ID + "/activity"
	return activitystream.NewCreate(createID, note.AttributedTo, note.To, note.Cc, note)
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

	r.Handler(http.MethodGet, "/.well-known/webfinger", httperror.HandleFuncWithError(webfingerHandler))
	r.Handler(http.MethodGet, "/.well-known/host-meta", httperror.HandleFuncWithError(hostMetaHandler))

	if config.IsDevelopment() {
		http.ListenAndServe("localhost:8080", r)
	} else {
		algnhsa.ListenAndServe(r, &algnhsa.Options{RequestType: algnhsa.RequestTypeAPIGatewayV1})
	}
}
