package main

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nna774/s.nna774.net/config"
)

// multipart/form-data では r.ParseForm() だけだとボディが読まれず PostForm
// が空のままになる (ファイル部分を読むには ParseMultipartForm が要る)。
// 画像添付を足すのに合わせて multipart 対応したので、テキストフィールドも
// 引き続き読めることを確かめる。
func TestParseStatusRequestReadsMultipartFields(t *testing.T) {
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	_ = mw.WriteField("content", "やっほい")
	_ = mw.WriteField("visibility", "unlisted")
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/u/nana/statuses", body)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	got, err := parseStatusRequest(req)
	if err != nil {
		t.Fatalf("parseStatusRequest: %v", err)
	}
	if got.Content != "やっほい" {
		t.Errorf("Content = %q, want %q", got.Content, "やっほい")
	}
	if got.Visibility != "unlisted" {
		t.Errorf("Visibility = %q, want %q", got.Visibility, "unlisted")
	}
}

// image フィールドを含まない通常の form-urlencoded な投稿 (curl 等) まで
// FormFile 経由で弾いてしまわないことを確かめる。multipart 以外では
// FormFile が "request Content-Type isn't multipart/form-data" を返すため、
// 呼び出し自体を避けなければならない。
func TestImageAttachmentFromRequestIgnoresNonMultipart(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/u/nana/statuses", strings.NewReader("content=hi"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	attachment, herr := imageAttachmentFromRequest(req.Context(), req)
	if herr != nil {
		t.Fatalf("imageAttachmentFromRequest: %v", herr)
	}
	if attachment != nil {
		t.Errorf("attachment = %+v, want nil", attachment)
	}
}

// multipart だが image フィールドが無ければ何もしない。
func TestImageAttachmentFromRequestNoFile(t *testing.T) {
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	_ = mw.WriteField("content", "hi")
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/u/nana/statuses", body)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	attachment, herr := imageAttachmentFromRequest(req.Context(), req)
	if herr != nil {
		t.Fatalf("imageAttachmentFromRequest: %v", herr)
	}
	if attachment != nil {
		t.Errorf("attachment = %+v, want nil", attachment)
	}
}

// Gyazo のトークンを設定し忘れたまま画像付き投稿が来た場合、静かに無視せず
// エラーを返さなければならない。さもないと画像だけ消えた投稿になる。
func TestImageAttachmentFromRequestWithoutGyazoToken(t *testing.T) {
	old := Config
	Config = &config.Config{}
	defer func() { Config = old }()

	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	_ = mw.WriteField("content", "hi")
	part, err := mw.CreateFormFile("image", "a.png")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	_, _ = part.Write([]byte("fake-image-bytes"))
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/u/nana/statuses", body)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	if _, herr := imageAttachmentFromRequest(req.Context(), req); herr == nil {
		t.Error("imageAttachmentFromRequest succeeded, want error (no gyazo token configured)")
	}
}
