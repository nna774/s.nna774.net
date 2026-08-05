package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUploadToGyazo(t *testing.T) {
	var gotAccessToken, gotFilename string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("server: ParseMultipartForm: %v", err)
		}
		gotAccessToken = r.FormValue("access_token")
		if _, h, err := r.FormFile("imagedata"); err == nil {
			gotFilename = h.Filename
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"image_id":"abc","permalink_url":"https://gyazo.com/abc","url":"https://i.gyazo.com/abc.png","type":"png"}`))
	}))
	defer srv.Close()

	old := gyazoUploadURL
	gyazoUploadURL = srv.URL
	defer func() { gyazoUploadURL = old }()

	result, err := uploadToGyazo(context.Background(), "test-token", "screenshot.png", strings.NewReader("fake-image-bytes"))
	if err != nil {
		t.Fatalf("uploadToGyazo: %v", err)
	}
	if gotAccessToken != "test-token" {
		t.Errorf("access_token sent = %q, want %q", gotAccessToken, "test-token")
	}
	if gotFilename != "screenshot.png" {
		t.Errorf("filename sent = %q, want %q", gotFilename, "screenshot.png")
	}
	if result.URL != "https://i.gyazo.com/abc.png" {
		t.Errorf("URL = %q", result.URL)
	}
	if result.MediaType != "image/png" {
		t.Errorf("MediaType = %q, want image/png", result.MediaType)
	}
}

// アクセストークンが無効な場合など、Gyazo が非 200 を返したらエラーに
// しなければならない。ここを見落とすと、失敗した投稿の Attachment に
// 空の URL が載ってしまう。
func TestUploadToGyazoRejectsErrorResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"invalid access_token"}`))
	}))
	defer srv.Close()

	old := gyazoUploadURL
	gyazoUploadURL = srv.URL
	defer func() { gyazoUploadURL = old }()

	if _, err := uploadToGyazo(context.Background(), "bad-token", "a.png", strings.NewReader("x")); err == nil {
		t.Error("uploadToGyazo succeeded, want error")
	}
}
