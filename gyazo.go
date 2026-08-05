package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"path"
	"strings"
)

// gyazoUploadURL は var にしてある。テストから httptest サーバへ差し替える
// ため。
var gyazoUploadURL = "https://upload.gyazo.com/api/upload"

type gyazoUploadResult struct {
	URL       string
	MediaType string
}

// quoteEscaper は mime/multipart.CreateFormFile 内部にある同名の未公開
// ヘルパーと同じ規則。Content-Disposition の filename に使う。
var quoteEscaper = strings.NewReplacer("\\", "\\\\", `"`, "\\\"")

// uploadToGyazo は画像データを Gyazo にアップロードし、直リンクの URL を
// 返す。自前で画像ストレージ (S3 等) を持たずに投稿へ画像を添付するための
// 手段として、まずは Gyazo に投げる。
func uploadToGyazo(ctx context.Context, accessToken string, filename string, contentType string, data io.Reader) (*gyazoUploadResult, error) {
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	if err := mw.WriteField("access_token", accessToken); err != nil {
		return nil, err
	}
	// mw.CreateFormFile だと part の Content-Type が常に
	// application/octet-stream に固定される。Gyazo はそれだと
	// "Not an Image" で 400 を返すため、実際の画像の Content-Type を
	// 自分でヘッダに積む。
	if contentType == "" {
		if t := mime.TypeByExtension(path.Ext(filename)); t != "" {
			contentType = t
		} else {
			contentType = "application/octet-stream"
		}
	}
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="imagedata"; filename="%s"`, quoteEscaper.Replace(filename)))
	h.Set("Content-Type", contentType)
	part, err := mw.CreatePart(h)
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(part, data); err != nil {
		return nil, err
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, gyazoUploadURL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gyazo upload request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("gyazo upload failed: %v: %s", resp.Status, b)
	}

	var out struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("gyazo upload: decoding response failed: %w", err)
	}
	if out.URL == "" {
		return nil, errors.New("gyazo upload: response had no url")
	}

	// レスポンスは type (拡張子) も返すが、mediaType には拡張子から
	// 導いた MIME を使う。IconMediaType と同じ流儀。
	mediaType := "application/octet-stream"
	if u, err := url.Parse(out.URL); err == nil {
		if t := mime.TypeByExtension(path.Ext(u.Path)); t != "" {
			mediaType = t
		}
	}

	return &gyazoUploadResult{URL: out.URL, MediaType: mediaType}, nil
}
