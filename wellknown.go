package main

import (
	"errors"
	"net/http"

	"github.com/nna774/s.nna774.net/datastore"
	"github.com/nna774/s.nna774.net/httperror"
)

// nodeinfo は他インスタンスや統計サイトがソフトウェアの種類を知るための
// 規約。https://nodeinfo.diaspora.software/
type nodeInfoLink struct {
	Rel  string `json:"rel"`
	Href string `json:"href"`
}

type nodeInfoIndex struct {
	Links []nodeInfoLink `json:"links"`
}

type nodeInfoSoftware struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type nodeInfoUsers struct {
	Total int `json:"total"`
}

type nodeInfoUsage struct {
	Users      nodeInfoUsers `json:"users"`
	LocalPosts int           `json:"localPosts"`
}

type nodeInfo struct {
	Version           string                 `json:"version"`
	Software          nodeInfoSoftware       `json:"software"`
	Protocols         []string               `json:"protocols"`
	Services          map[string][]string    `json:"services"`
	OpenRegistrations bool                   `json:"openRegistrations"`
	Usage             nodeInfoUsage          `json:"usage"`
	Metadata          map[string]interface{} `json:"metadata"`
}

// softwareVersion はビルド時に上書きできる。
var softwareVersion = "dev"

func nodeInfoIndexHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	return respondJSONWithoutActivityType(w, http.StatusOK, nodeInfoIndex{
		Links: []nodeInfoLink{{
			Rel:  "http://nodeinfo.diaspora.software/ns/schema/2.1",
			Href: Config.Origin + "/nodeinfo/2.1",
		}},
	})
}

func nodeInfoHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	posts, err := client.Top(r.Context(), outboxKey)
	if err != nil && !errors.Is(err, datastore.ErrNotFound) {
		logf("counting posts for nodeinfo failed: %v", err)
	}
	// nodeinfo 2.1 の Content-Type は profile 付きが正式。
	w.Header().Set("Content-Type",
		`application/json; profile="http://nodeinfo.diaspora.software/ns/schema/2.1#"`)
	return respondJSONWithoutActivityType(w, http.StatusOK, nodeInfo{
		Version:           "2.1",
		Software:          nodeInfoSoftware{Name: "s.nna774.net", Version: softwareVersion},
		Protocols:         []string{"activitypub"},
		Services:          map[string][]string{"inbound": {}, "outbound": {}},
		OpenRegistrations: false,
		Usage: nodeInfoUsage{
			Users:      nodeInfoUsers{Total: 1},
			LocalPosts: posts,
		},
		Metadata: map[string]interface{}{
			"nodeName":        Config.Name,
			"nodeDescription": Config.Summary,
		},
	})
}

func hostMetaHandler(w http.ResponseWriter, r *http.Request) httperror.HttpError {
	// XRD は XML なので application/json ではない。以前は respondAsJSON を
	// 経由していないのに Content-Type が JSON になっていた。
	w.Header().Set("Content-Type", "application/xrd+xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<XRD xmlns="http://docs.oasis-open.org/ns/xri/xrd-1.0">
  <Link rel="lrdd" template="` + Config.Origin + `/.well-known/webfinger?resource={uri}"/>
</XRD>
`))
	return nil
}
