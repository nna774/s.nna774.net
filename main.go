package main

import (
	"net/http"

	"github.com/akrylysov/algnhsa"
)

func indexHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("hello"))
}

func main() {
	http.HandleFunc("/", indexHandler)
	algnhsa.ListenAndServe(nil, &algnhsa.Options{RequestType: algnhsa.RequestTypeAPIGateway})
}
