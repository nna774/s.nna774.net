package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUploadToGyazo(t *testing.T) {
	var gotAccessToken, gotFilename, gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("server: ParseMultipartForm: %v", err)
		}
		gotAccessToken = r.FormValue("access_token")
		if _, h, err := r.FormFile("imagedata"); err == nil {
			gotFilename = h.Filename
			gotContentType = h.Header.Get("Content-Type")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"image_id":"abc","permalink_url":"https://gyazo.com/abc","url":"https://i.gyazo.com/abc.png","type":"png"}`))
	}))
	defer srv.Close()

	old := gyazoUploadURL
	gyazoUploadURL = srv.URL
	defer func() { gyazoUploadURL = old }()

	result, err := uploadToGyazo(context.Background(), "test-token", "screenshot.png", "image/png", strings.NewReader("fake-image-bytes"))
	if err != nil {
		t.Fatalf("uploadToGyazo: %v", err)
	}
	if gotAccessToken != "test-token" {
		t.Errorf("access_token sent = %q, want %q", gotAccessToken, "test-token")
	}
	if gotFilename != "screenshot.png" {
		t.Errorf("filename sent = %q, want %q", gotFilename, "screenshot.png")
	}
	// mime/multipart.CreateFormFile を使うと part の Content-Type が常に
	// application/octet-stream に固定され、Gyazo に "Not an Image" として
	// 蹴られる。実際の画像の Content-Type がそのまま送られていることを
	// 確かめる。
	if gotContentType != "image/png" {
		t.Errorf("part Content-Type = %q, want %q", gotContentType, "image/png")
	}
	if result.URL != "https://i.gyazo.com/abc.png" {
		t.Errorf("URL = %q", result.URL)
	}
	if result.MediaType != "image/png" {
		t.Errorf("MediaType = %q, want image/png", result.MediaType)
	}
}

// ブラウザが Content-Type を送ってこなかった場合でも、拡張子から
// 推測した MIME を使い application/octet-stream 送りっぱなしにはしない。
func TestUploadToGyazoGuessesContentTypeFromFilename(t *testing.T) {
	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("server: ParseMultipartForm: %v", err)
		}
		if _, h, err := r.FormFile("imagedata"); err == nil {
			gotContentType = h.Header.Get("Content-Type")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"url":"https://i.gyazo.com/abc.jpg","type":"jpg"}`))
	}))
	defer srv.Close()

	old := gyazoUploadURL
	gyazoUploadURL = srv.URL
	defer func() { gyazoUploadURL = old }()

	if _, err := uploadToGyazo(context.Background(), "test-token", "photo.jpg", "", strings.NewReader("fake-image-bytes")); err != nil {
		t.Fatalf("uploadToGyazo: %v", err)
	}
	if !strings.HasPrefix(gotContentType, "image/jpeg") {
		t.Errorf("part Content-Type = %q, want image/jpeg", gotContentType)
	}
}

// iPhone から HEIC のまま upload された場合、mime.TypeByExtension は
// .heic を知らず application/octet-stream になってしまい、タイムライン上
// では単なる添付ファイルリンクとしてしか表示できない。Gyazo の JPEG
// サムネイル URL に差し替えて image として表示できるようにする。
// サムネイルの幅は oEmbed で分かる原寸の幅に合わせる (固定 1000px だと
// 荒いため)。
func TestUploadToGyazoRewritesHEICToThumbnail(t *testing.T) {
	oembedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("url"); got != "https://gyazo.com/abc" {
			t.Errorf("oembed request url = %q, want %q", got, "https://gyazo.com/abc")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"width":3024,"height":4032}`))
	}))
	defer oembedSrv.Close()
	oldOembed := gyazoOembedURL
	gyazoOembedURL = oembedSrv.URL
	defer func() { gyazoOembedURL = oldOembed }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("server: ParseMultipartForm: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"image_id":"abc","permalink_url":"https://gyazo.com/abc","url":"https://i.gyazo.com/e775974cff4ec68396c3bfc85b06aea4.heic","type":"heic"}`))
	}))
	defer srv.Close()

	old := gyazoUploadURL
	gyazoUploadURL = srv.URL
	defer func() { gyazoUploadURL = old }()

	result, err := uploadToGyazo(context.Background(), "test-token", "IMG_0001.heic", "image/heic", strings.NewReader("fake-heic-bytes"))
	if err != nil {
		t.Fatalf("uploadToGyazo: %v", err)
	}
	wantURL := "https://i.gyazo.com/thumb/3024/e775974cff4ec68396c3bfc85b06aea4-heic.jpg"
	if result.URL != wantURL {
		t.Errorf("URL = %q, want %q", result.URL, wantURL)
	}
	if result.MediaType != "image/jpeg" {
		t.Errorf("MediaType = %q, want image/jpeg", result.MediaType)
	}
}

// oEmbed の取得に失敗しても (Gyazo 側の一時的な不調など) アップロード
// 自体は失敗させず、固定幅のサムネイルにフォールバックする。
func TestUploadToGyazoFallsBackToDefaultThumbnailWidthWhenOembedFails(t *testing.T) {
	oembedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer oembedSrv.Close()
	oldOembed := gyazoOembedURL
	gyazoOembedURL = oembedSrv.URL
	defer func() { gyazoOembedURL = oldOembed }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("server: ParseMultipartForm: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"image_id":"abc","permalink_url":"https://gyazo.com/abc","url":"https://i.gyazo.com/e775974cff4ec68396c3bfc85b06aea4.heic","type":"heic"}`))
	}))
	defer srv.Close()

	old := gyazoUploadURL
	gyazoUploadURL = srv.URL
	defer func() { gyazoUploadURL = old }()

	result, err := uploadToGyazo(context.Background(), "test-token", "IMG_0001.heic", "image/heic", strings.NewReader("fake-heic-bytes"))
	if err != nil {
		t.Fatalf("uploadToGyazo: %v", err)
	}
	wantURL := "https://i.gyazo.com/thumb/1000/e775974cff4ec68396c3bfc85b06aea4-heic.jpg"
	if result.URL != wantURL {
		t.Errorf("URL = %q, want %q", result.URL, wantURL)
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

	if _, err := uploadToGyazo(context.Background(), "bad-token", "a.png", "image/png", strings.NewReader("x")); err == nil {
		t.Error("uploadToGyazo succeeded, want error")
	}
}

func TestGyazoOembedWidth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("url"); got != "https://gyazo.com/abc" {
			t.Errorf("request url = %q, want %q", got, "https://gyazo.com/abc")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"width":5712,"height":2000}`))
	}))
	defer srv.Close()

	old := gyazoOembedURL
	gyazoOembedURL = srv.URL
	defer func() { gyazoOembedURL = old }()

	got, err := gyazoOembedWidth(context.Background(), "https://gyazo.com/abc")
	if err != nil {
		t.Fatalf("gyazoOembedWidth: %v", err)
	}
	if got != 5712 {
		t.Errorf("width = %d, want 5712", got)
	}
}

func TestGyazoOembedWidthErrorsOnNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	old := gyazoOembedURL
	gyazoOembedURL = srv.URL
	defer func() { gyazoOembedURL = old }()

	if _, err := gyazoOembedWidth(context.Background(), "https://gyazo.com/abc"); err == nil {
		t.Error("gyazoOembedWidth succeeded, want error")
	}
}

func TestGyazoImagePageURL(t *testing.T) {
	tests := []struct {
		name   string
		rawURL string
		want   string
		wantOK bool
	}{
		{
			name:   "直リンク",
			rawURL: "https://i.gyazo.com/1234567890abcdef1234567890abcdef.png",
			want:   "https://gyazo.com/1234567890abcdef1234567890abcdef",
			wantOK: true,
		},
		{
			name:   "HEIC サムネイル",
			rawURL: "https://i.gyazo.com/thumb/1000/1234567890abcdef1234567890abcdef-heic.jpg",
			want:   "https://gyazo.com/1234567890abcdef1234567890abcdef",
			wantOK: true,
		},
		{
			name:   "gyazo 以外のホスト",
			rawURL: "https://example.com/1234567890abcdef1234567890abcdef.png",
			wantOK: false,
		},
		{
			name:   "不正な URL",
			rawURL: "://not-a-url",
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := gyazoImagePageURL(tt.rawURL)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && got != tt.want {
				t.Errorf("gyazoImagePageURL(%q) = %q, want %q", tt.rawURL, got, tt.want)
			}
		})
	}
}
