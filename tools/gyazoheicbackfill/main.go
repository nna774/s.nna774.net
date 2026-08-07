// gyazoheicbackfill は、旧実装が固定 1000px で保存した HEIC サムネイル URL
// (https://i.gyazo.com/thumb/1000/<hash>-heic.jpg) を、Gyazo の oEmbed で
// 分かる原寸の幅に合わせた大きいサムネイルへ差し替える一回限りの移行ツール。
// 新規アップロードはすでに gyazo.go 側が oEmbed の幅を使うため、これは
// 既に保存済みの投稿だけを対象にする。
//
// status と outbox の両方に同じ Note が重複して保存されているため、両方を
// 更新する。
//
//	go run ./tools/gyazoheicbackfill                         # 本番に対して dry-run
//	go run ./tools/gyazoheicbackfill -write                  # 実際に書き換える
//	go run ./tools/gyazoheicbackfill -endpoint http://localhost:8000 -write  # ローカル検証
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"

	"github.com/nna774/s.nna774.net/activitystream"
	"github.com/nna774/s.nna774.net/datastore"
)

// status / outbox に Note を保存する際のキー。main パッケージ側の
// statusKey / outboxKey と同じ値 (このツールは main パッケージを import
// できないためリテラルで持つ)。
const (
	statusKey = "status"
	outboxKey = "outbox"
)

var thumbPattern = regexp.MustCompile(`^https://i\.gyazo\.com/thumb/1000/([0-9a-f]+)-heic\.jpg$`)

func main() {
	region := flag.String("region", "ap-northeast-1", "AWS region")
	table := flag.String("table", "s-nna774-net", "DynamoDB table name")
	kvTable := flag.String("kv-table", "s-nna774-net-kv", "DynamoDB kv table name (datastore.NewClient が要求するだけで使わない)")
	endpoint := flag.String("endpoint", "", "DynamoDB endpoint override (dynamodb-local 検証用)")
	oembedURL := flag.String("oembed-url", "https://api.gyazo.com/api/oembed", "Gyazo oEmbed API のURL")
	write := flag.Bool("write", false, "実際に書き換える。指定しなければ対象を表示するだけの dry-run")
	flag.Parse()

	ctx := context.Background()
	client, err := datastore.NewClient(ctx, *region, *table, *kvTable, *endpoint)
	if err != nil {
		log.Fatalf("connecting to dynamodb: %v", err)
	}

	entries, err := client.TakeEntries(ctx, statusKey, datastore.Inf, 1_000_000, datastore.Desc)
	if err != nil {
		log.Fatalf("listing statuses: %v", err)
	}

	updated := 0
	for _, e := range entries {
		changes, ok := upgradeAttachments(ctx, *oembedURL, e.Object.Attachment)
		if !ok {
			continue
		}
		for old, neu := range changes {
			fmt.Printf("status %d: %s -> %s\n", e.ID, old, neu)
		}
		updated++
		if !*write {
			continue
		}
		if err := client.Put(ctx, statusKey, e.ID, e.Object); err != nil {
			log.Fatalf("updating status %d: %v", e.ID, err)
		}
		if err := upgradeOutbox(ctx, client, *oembedURL, e.ID); err != nil {
			log.Fatalf("updating outbox %d: %v", e.ID, err)
		}
	}
	fmt.Printf("%d/%d statuses updated (write=%v)\n", updated, len(entries), *write)
}

// upgradeAttachments は attachments のうち固定 1000px の HEIC サムネイル
// URL を、oEmbed で分かる原寸相当の幅に差し替える。変更した URL の対応
// (旧 -> 新) と、1件でも変更したかを返す。oEmbed で 1000px 以下の幅しか
// 分からなかった場合や取得に失敗した場合はそのまま変更しない。
func upgradeAttachments(ctx context.Context, oembedURL string, attachments activitystream.Objects) (map[string]string, bool) {
	changed := map[string]string{}
	for _, a := range attachments {
		m := thumbPattern.FindStringSubmatch(a.URL)
		if m == nil {
			continue
		}
		hash := m[1]
		width, err := oembedWidth(ctx, oembedURL, "https://gyazo.com/"+hash)
		if err != nil || width <= 1000 {
			continue
		}
		newURL := fmt.Sprintf("https://i.gyazo.com/thumb/%d/%s-heic.jpg", width, hash)
		changed[a.URL] = newURL
		a.URL = newURL
	}
	return changed, len(changed) > 0
}

// upgradeOutbox は outbox に保存された Create Activity の中の Note を
// 同じ内容で更新する。status と outbox は投稿時に同じ Note を重複して
// 保存しているため、片方だけ直すと GET /u/:user/outbox の表示が古いまま
// になる。
func upgradeOutbox(ctx context.Context, client datastore.Client, oembedURL string, id int) error {
	create, err := client.GetObject(ctx, outboxKey, id)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return nil
		}
		return err
	}
	note := create.Object.Item()
	if note == nil {
		return nil
	}
	if _, ok := upgradeAttachments(ctx, oembedURL, note.Attachment); !ok {
		return nil
	}
	return client.Put(ctx, outboxKey, id, create)
}

func oembedWidth(ctx context.Context, oembedURL, pageURL string) (int, error) {
	u, err := url.Parse(oembedURL)
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
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("gyazo oembed failed: %s", resp.Status)
	}
	var out struct {
		Width int `json:"width"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, err
	}
	if out.Width <= 0 {
		return 0, errors.New("gyazo oembed: response had no width")
	}
	return out.Width, nil
}
