package main

import (
	"bytes"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"sync"

	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/julienschmidt/httprouter"
	"github.com/nna774/s.nna774.net/activitystream"
	"github.com/nna774/s.nna774.net/config"
	"github.com/nna774/s.nna774.net/httperror"
	"github.com/nna774/s.nna774.net/webfinger"

	"github.com/akrylysov/algnhsa"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/go-fed/httpsig"
	"github.com/guregu/dynamo"
	"golang.org/x/exp/slices"
)

const configFile = "config.yml"

var TableName = os.Getenv("DYNAMODB_TABLE_NAME")

var Config *config.Config
var signer *mySigner

func init() {
	cnf, err := config.LoadConfig(configFile)
	if err != nil {
		panic(err)
	}
	Config = cnf

	signer, err = newSigner(Config.PrivateKey(), Config.PublicKey(), Config.ID()+"#main-key")
	if err != nil {
		panic(err)
	}
}

type mySigner struct {
	signer    httpsig.Signer
	mu        *sync.Mutex
	privKey   *rsa.PrivateKey
	publicKey string
	keyID     string
}

func newSigner(privKey *rsa.PrivateKey, publicKey string, keyID string) (*mySigner, error) {
	signer, _, err := httpsig.NewSigner(nil, httpsig.DigestSha256, []string{httpsig.RequestTarget, "Date", "Digest"}, httpsig.Signature, int64(time.Minute))
	if err != nil {
		return nil, err
	}
	return &mySigner{
		signer:    signer,
		mu:        &sync.Mutex{},
		privKey:   privKey,
		publicKey: publicKey,
		keyID:     keyID,
	}, nil
}

func (s *mySigner) requestWithSign(method string, url string, body []byte) (*http.Response, error) {
	req, err := http.NewRequest(method, url, bytes.NewBuffer(body))
	if err != nil {
		return nil, err
	}
	req.Header.Add("content-type", activitystream.ActivityStreamsContentType)
	req.Header.Add("date", CurrentTime())
	s.mu.Lock()
	defer s.mu.Unlock()
	err = s.signer.SignRequest(s.privKey, s.keyID, req, body)
	if err != nil {
		return nil, err
	}
	return http.DefaultClient.Do(req)
}

func respondAsJSON(w http.ResponseWriter, status int, body interface{}) {
	w.WriteHeader(status)
	e := json.NewEncoder(w)
	e.SetIndent("", "  ")
	e.Encode(body)
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
	respondAsJSON(w, http.StatusOK, resp)
	return nil
}

func jsonUserHander(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	resp := activitystream.NewUserResource(
		Config.ID(), Config.Name, Config.IconURI, Config.IconMediaType(), Config.LocalPart(), Config.ID()+"/inbox", Config.ID()+"/outbox", Config.ID()+"/followers", Config.ID()+"/following", "やっぴ〜", Config.ID()+"#main-key", Config.PublicKey())
	respondAsJSON(w, http.StatusOK, &resp)
	return nil
}

func userHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	if strings.Contains(r.Header.Get("accept"), "json") {
		return jsonUserHander(w, r)
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("hello! " + r.URL.Path))
	return nil
}

func table() (*dynamo.Table, error) {
	cfg := aws.NewConfig()
	s, err := session.NewSession()
	if err != nil {
		return nil, err
	}
	db := dynamo.New(s, cfg)
	t := db.Table(TableName)
	return &t, nil
}

func sendToInbox[T activitystream.Inbox](to string, object T) error {
	buf := &bytes.Buffer{}
	err := json.NewEncoder(buf).Encode(object)
	if err != nil {
		return err
	}
	ur, err := activitystream.FetchActorInfo(to)
	if err != nil {
		return err
	}
	resp, err := signer.requestWithSign(http.MethodPost, ur.Inbox, buf.Bytes())
	log.Printf("send: %+v, body: %s, err: %+v", resp, resp.Body, err)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

func sendAccept(act activitystream.Activity) error {
	accept := activitystream.NewAccept(act, Config.ID(), Config.Origin+"/accept/"+fmt.Sprintf("%v", time.Now().Unix()))
	return sendToInbox(accept.Actor, accept)
}

func sendNote(to string, note activitystream.Object) error {
	createID := note.ID + "/activity"
	create := activitystream.NewCreate(createID, note.AttributedTo, note.To, note.Cc, note)
	return sendToInbox(to, create)
}

func followHandler(w http.ResponseWriter, r *http.Request, in activitystream.ReceivedInbox) httperror.HttpError {
	// TODO: allow only
	object, err := in.Item.MarshalJSON()
	if err != nil {
		return httperror.StatusUnprocessableEntity("followHandler", err)
	}
	obj := strings.Trim(string(object), "\"")
	if obj != Config.ID() {
		return httperror.StatusUnprocessableEntity(fmt.Sprintf("followHandler: unexpected object: %s", object), nil)
	}
	respondText(w, http.StatusCreated, "created\n")
	go sendAccept(in.Activity) // TODO: err
	return nil
}

func inboxHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	// get の時の処理

	var in activitystream.ReceivedInbox
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

func CurrentTime() string {
	return strings.ReplaceAll(time.Now().UTC().Format(time.RFC1123), "UTC", "GMT")
}

func main() {
	r := httprouter.New()
	r.Handler(http.MethodGet, "/", httperror.HandleFuncWithError(indexHandler))
	r.Handler(http.MethodGet, "/u/:user", httperror.HandleFuncWithError(userHandler))
	r.Handler(http.MethodPost, "/u/:user/inbox", httperror.HandleFuncWithError(inboxHandler))

	r.Handler(http.MethodGet, "/.well-known/webfinger", httperror.HandleFuncWithError(webfingerHandler))
	r.Handler(http.MethodGet, "/.well-known/host-meta", httperror.HandleFuncWithError(hostMetaHandler))

	if os.Getenv("ENV") == "development" {
		http.ListenAndServe(":8080", r)
	} else {
		algnhsa.ListenAndServe(r, &algnhsa.Options{RequestType: algnhsa.RequestTypeAPIGateway})
	}
}
