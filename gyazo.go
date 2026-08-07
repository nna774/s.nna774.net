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

// gyazoUploadURL / gyazoOembedURL は var にしてある。テストから httptest
// サーバへ差し替えるため。
var gyazoUploadURL = "https://upload.gyazo.com/api/upload"
var gyazoOembedURL = "https://api.gyazo.com/api/oembed"

// defaultHEICThumbnailWidth は oEmbed から原寸の幅が取れなかった場合に
// 使うフォールバック値。
const defaultHEICThumbnailWidth = 1000

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
		URL          string `json:"url"`
		Type         string `json:"type"`
		PermalinkURL string `json:"permalink_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("gyazo upload: decoding response failed: %w", err)
	}
	if out.URL == "" {
		return nil, errors.New("gyazo upload: response had no url")
	}

	// HEIC は多くのブラウザがそのまま <img> に出せない (Go の mime パッケージ
	// も拡張子テーブルに .heic を持たず application/octet-stream に落ちる)。
	// この場合だけ Gyazo が生成する JPEG サムネイルの URL に差し替えて、
	// タイムライン上で普通の画像として表示できるようにする。それ以外の
	// 形式 (png/jpg/gif 等) はそのまま直リンクを使う。
	resultURL := out.URL
	mediaType := "application/octet-stream"
	if isHEICUpload(out.Type, out.URL) {
		width := defaultHEICThumbnailWidth
		if out.PermalinkURL != "" {
			// oEmbed で原寸の幅が分かれば、それに合わせたサムネイルを
			// 要求する。固定 1000px だと荒くて読みにくいため。取得に
			// 失敗してもアップロード自体は失敗させず、フォールバック幅
			// で続行する。
			if w, err := gyazoOembedWidth(ctx, out.PermalinkURL); err == nil {
				width = w
			}
		}
		if thumb, ok := gyazoHEICThumbnailURL(out.URL, width); ok {
			resultURL = thumb
			mediaType = "image/jpeg"
		}
	} else if u, err := url.Parse(out.URL); err == nil {
		if t := mime.TypeByExtension(path.Ext(u.Path)); t != "" {
			mediaType = t
		}
	}

	return &gyazoUploadResult{URL: resultURL, MediaType: mediaType}, nil
}

// isHEICUpload は Gyazo のレスポンスが HEIC/HEIF 画像かどうかを判定する。
// type フィールドを優先し、無い場合は URL の拡張子で判定する。
func isHEICUpload(typ, rawURL string) bool {
	switch strings.ToLower(typ) {
	case "heic", "heif":
		return true
	case "":
		// type が無い古いレスポンス互換のため、URL の拡張子でも判定する。
	default:
		return false
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimPrefix(path.Ext(u.Path), ".")) {
	case "heic", "heif":
		return true
	default:
		return false
	}
}

// gyazoHEICThumbnailURL は HEIC の直リンク URL (https://i.gyazo.com/<hash>.heic)
// から、そのまま表示できる JPEG サムネイルの URL を組み立てる。width には
// 原寸相当のサムネイルを要求するため oEmbed から取った幅を渡す。
// 例: https://i.gyazo.com/thumb/1179/<hash>-heic.jpg
func gyazoHEICThumbnailURL(rawURL string, width int) (string, bool) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", false
	}
	base := path.Base(u.Path)
	hash := strings.TrimSuffix(base, path.Ext(base))
	if hash == "" {
		return "", false
	}
	return fmt.Sprintf("https://i.gyazo.com/thumb/%d/%s-heic.jpg", width, hash), true
}

// gyazoOembedWidth は Gyazo の oEmbed API から画像の原寸の幅を取る。
// HEIC のサムネイルを常に固定 1000px で出すと荒くなるため、原寸相当の
// サムネイルを組み立てるのに使う。
func gyazoOembedWidth(ctx context.Context, pageURL string) (int, error) {
	u, err := url.Parse(gyazoOembedURL)
	if err != nil {
		return 0, err
	}
	q := u.Query()
	q.Set("url", pageURL)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return 0, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("gyazo oembed request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("gyazo oembed failed: %v: %s", resp.Status, b)
	}

	var out struct {
		Width int `json:"width"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, fmt.Errorf("gyazo oembed: decoding response failed: %w", err)
	}
	if out.Width <= 0 {
		return 0, errors.New("gyazo oembed: response had no width")
	}
	return out.Width, nil
}

// gyazoImagePageURL は Gyazo にアップロードした画像の直リンク URL から、
// ブラウザで見る画像ページ (https://gyazo.com/<hash>) の URL を組み立てる。
// タイムライン上のサムネイルは小さく表示されるため、クリックで原寸の
// 画像ページへ飛べるようにするために使う。i.gyazo.com 以外の URL の場合は
// false を返す。
func gyazoImagePageURL(rawURL string) (string, bool) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host != "i.gyazo.com" {
		return "", false
	}
	base := path.Base(u.Path)
	hash := strings.TrimSuffix(base, path.Ext(base))
	// /thumb/<size>/<hash>-heic のようなサムネイル URL は末尾に type の
	// ヒントが付くので削る。
	if strings.HasPrefix(u.Path, "/thumb/") {
		if i := strings.LastIndex(hash, "-"); i >= 0 {
			hash = hash[:i]
		}
	}
	if hash == "" {
		return "", false
	}
	return "https://gyazo.com/" + hash, true
}
