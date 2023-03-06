package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

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
	"golang.org/x/exp/slices"
)

const configFile = "config.yml"

var region = "ap-northeast-1" //
var tableName = os.Getenv("DYNAMODB_TABLE_NAME")

var Config *config.Config
var signer *httpsigclient.Signer
var client datastore.Client

func init() {
	cnf, err := config.LoadConfig(configFile)
	if err != nil {
		panic(err)
	}
	Config = cnf

	signer, err = httpsigclient.NewSigner(Config.PrivateKey(), Config.PublicKey(), Config.ID()+"#main-key")
	if err != nil {
		panic(err)
	}

	client, err = datastore.NewClient(region, tableName)
	if err != nil {
		panic(err)
	}
}

func inboxURI() string     { return Config.ID() + "/inbox" }
func outboxURI() string    { return Config.ID() + "/outbox" }
func followersURI() string { return Config.ID() + "/followers" }
func followingURI() string { return Config.ID() + "/following" }
func mainKeyURI() string   { return Config.ID() + "#main-key" }

func respondAsJSON(w http.ResponseWriter, status int, body interface{}) httperror.HttpError {
	buf := &bytes.Buffer{}
	e := json.NewEncoder(buf)
	e.SetIndent("", "  ")
	err := e.Encode(body)
	if err != nil {
		return httperror.StatusInternalServerError("json encode failed", err)
	}
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

func sendToInbox(to string, object *activitystream.Object) error {
	buf := &bytes.Buffer{}
	err := json.NewEncoder(buf).Encode(object)
	if err != nil {
		return err
	}
	ur, err := activitystream.FetchActorInfo(to)
	if err != nil {
		return err
	}
	resp, err := signer.RequestWithSign(http.MethodPost, ur.Inbox, buf.Bytes())
	log.Printf("send: %+v, body: %s, err: %+v", resp, resp.Body, err)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

func sendAccept(obj activitystream.Object, act activitystream.Activity) error {
	accept := activitystream.NewAccept(act, Config.ID(), Config.Origin+"/accept/"+fmt.Sprintf("%v", time.Now().Unix()))
	return sendToInbox(act.Actor, accept)
}

func sendNote(to string, note activitystream.Object) error {
	createID := note.ID + "/activity"
	create := activitystream.NewCreate(createID, note.AttributedTo, note.To, note.Cc, note)
	return sendToInbox(to, create)
}

func followHandler(w http.ResponseWriter, r *http.Request, in activitystream.Object) httperror.HttpError {
	// TODO: allow only
	act, ok := in.Activity()
	if !ok {
		return httperror.StatusUnprocessableEntity("conversion failed", nil)
	}
	if act.Item.ID != Config.ID() {
		return httperror.StatusUnprocessableEntity(fmt.Sprintf("followHandler: unexpected object: %v", act.Item), nil)
	}
	respondText(w, http.StatusCreated, "created\n")
	go sendAccept(in, act) // TODO: err
	return nil
}

func postInboxHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	var in activitystream.Object
	err := json.NewDecoder(r.Body).Decode(&in)
	if err != nil {
		return httperror.StatusUnprocessableEntity("decode failed", err)
	}
	switch strings.ToLower(in.Type) {
	case "follow":
		return followHandler(w, r, in)
	}
	respondText(w, http.StatusCreated, "created\n")
	return nil
}

const outboxKey = "outbox"

func outboxHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	itemsCnt, err := client.Top(outboxKey)
	if err != nil && err != datastore.ErrNotFound { // ErrNotFound の時は1つもitemが無い。
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
		items, err := client.TakeObject(outboxKey, datastore.Inf, defaultPerPage, datastore.Desc)
		log.Printf("items: %+v", items)
		if err != nil {
			return httperror.StatusInternalServerError("failed", err)
		}
		next := "next"
		prev := "prev"
		page = activitystream.NewOrderedCollectionPage(r.URL.String(), outboxURI(), next, prev, items)
	}

	return respondAsJSON(w, http.StatusOK, page)
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

func test(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	t, err := client.Top("test")
	log.Printf("t: %v, err: %v", t, err)
	t, err = client.Inc("test")
	log.Printf("t: %v, err: %v", t, err)
}

func main() {
	r := httprouter.New()
	r.Handler(http.MethodGet, "/", httperror.HandleFuncWithError(indexHandler))
	r.Handler(http.MethodGet, "/u/:user", httperror.HandleFuncWithError(userHandler))
	r.Handler(http.MethodPost, "/u/:user/inbox", httperror.HandleFuncWithError(postInboxHandler))
	r.Handler(http.MethodGet, "/u/:user/outbox", httperror.HandleFuncWithError(outboxHandler))
	r.Handler(http.MethodGet, "/u/:user/outbox/page", httperror.HandleFuncWithError(outboxPageHandler))

	r.Handler(http.MethodGet, "/.well-known/webfinger", httperror.HandleFuncWithError(webfingerHandler))
	r.Handler(http.MethodGet, "/.well-known/host-meta", httperror.HandleFuncWithError(hostMetaHandler))

	r.GET("/test", test)

	if os.Getenv("ENV") == "development" {
		http.ListenAndServe("localhost:8080", r)
	} else {
		algnhsa.ListenAndServe(r, &algnhsa.Options{RequestType: algnhsa.RequestTypeAPIGateway})
	}
}
