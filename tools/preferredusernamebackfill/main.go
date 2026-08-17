// preferredusernamebackfill は、PreferredUsername フィールド追加前に保存
// された followers / following / actorinfo キャッシュへ、リモート actor を
// 取得し直して preferredUsername を書き足す一回限りの移行ツール。
//
// PreferredUsername が無いレコードは /remote 以外のページ(timeline・
// notifications・followers/following 一覧など)で @user@host の代わりに
// actor の URI がそのまま表示される。followers/following はフォロー関係が
// 更新されるまで自然には直らないため、このツールで埋める。actorinfo は
// TTL 7日で自然に再取得されるが、待たずに直したい場合はこれも対象にする。
//
// すでに preferredUsername を持つ項目はスキップする。何度実行しても安全。
// リモートの actor が既に消えている等で取得に失敗した項目はログに出して
// 飛ばし、他の項目の処理は続ける。
//
//	go run ./tools/preferredusernamebackfill                         # 本番に対して dry-run
//	go run ./tools/preferredusernamebackfill -write                  # 実際に書き込む
//	go run ./tools/preferredusernamebackfill -endpoint http://localhost:8000 -write  # ローカル検証
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/nna774/s.nna774.net/activitystream"
	"github.com/nna774/s.nna774.net/config"
	"github.com/nna774/s.nna774.net/datastore"
	"github.com/nna774/s.nna774.net/httpsigclient"
)

const configFile = "config.yml"

// maxRemoteBody は actor.go の fetchObject と同じ上限。
const maxRemoteBody = 1 << 20

// fetchDelay はリモートサーバへの負荷を抑えるための1件ごとの間隔。
const fetchDelay = 200 * time.Millisecond

func main() {
	region := flag.String("region", "ap-northeast-1", "AWS region")
	table := flag.String("table", "s-nna774-net", "DynamoDB table name")
	kvTable := flag.String("kv-table", "s-nna774-net-kv", "DynamoDB kv table name")
	endpoint := flag.String("endpoint", "", "DynamoDB endpoint override (dynamodb-local 検証用)")
	write := flag.Bool("write", false, "実際に書き込む。指定しなければ対象を表示するだけの dry-run")
	flag.Parse()

	ctx := context.Background()

	cnf, err := config.LoadConfig(ctx, configFile, *region)
	if err != nil {
		log.Fatalf("loading config: %v", err)
	}

	client, err := datastore.NewClient(ctx, *region, *table, *kvTable, *endpoint)
	if err != nil {
		log.Fatalf("connecting to dynamodb: %v", err)
	}

	signers := map[string]*httpsigclient.Signer{}
	for _, a := range cnf.Actors {
		s, err := httpsigclient.NewSigner(a.PrivateKey(), a.PublicKey(), a.ID()+"#"+cnf.PublicKeyName)
		if err != nil {
			log.Fatalf("actor %v: building signer: %v", a.Username, err)
		}
		signers[a.LocalPart()] = s
	}

	var updated, skipped, failed int
	for _, a := range cnf.Actors {
		for _, pk := range []string{a.LocalPart() + ":" + datastore.KVFollowers, a.LocalPart() + ":" + datastore.KVFollowing} {
			u, s, f := backfillPartition(ctx, client, signers[a.LocalPart()], pk, *write)
			updated += u
			skipped += s
			failed += f
		}
	}
	// actorinfo はローカル actor に依存しない共有リソースなので1回だけ。
	// 取りに行く鍵は primary actor のものを使う (cacheActorInfo が実際に
	// 呼ばれる文脈の多くが primary actor である timeline/notifications
	// なため)。
	u, s, f := backfillPartition(ctx, client, signers[cnf.PrimaryActor().LocalPart()], datastore.KVActorInfo, *write)
	updated += u
	skipped += s
	failed += f

	fmt.Printf("done: %d updated, %d already had preferredUsername, %d failed (write=%v)\n", updated, skipped, failed, *write)
}

func backfillPartition(ctx context.Context, client datastore.Client, signer *httpsigclient.Signer, pk string, write bool) (updated, skipped, failed int) {
	items, err := client.QueryKV(ctx, pk)
	if err != nil {
		log.Fatalf("listing kv %s: %v", pk, err)
	}
	for _, it := range items {
		if it.PreferredUsername != "" {
			skipped++
			continue
		}
		actor, err := fetchActor(ctx, signer, it.SK)
		if err != nil {
			fmt.Printf("%s %s: fetch failed: %v\n", pk, it.SK, err)
			failed++
			continue
		}
		if actor.PreferredUsername == "" {
			fmt.Printf("%s %s: remote actor has no preferredUsername, skipping\n", pk, it.SK)
			failed++
			continue
		}
		fmt.Printf("%s %s: preferredUsername = %q\n", pk, it.SK, actor.PreferredUsername)
		updated++
		if write {
			moved := *it
			moved.PreferredUsername = actor.PreferredUsername
			if err := client.PutKV(ctx, &moved); err != nil {
				log.Fatalf("updating kv %s/%s: %v", pk, it.SK, err)
			}
		}
		time.Sleep(fetchDelay)
	}
	return updated, skipped, failed
}

// fetchActor は actor.go の fetchActor と同じ取得ロジック。このツールは
// main パッケージを import できないため複製している。
func fetchActor(ctx context.Context, signer *httpsigclient.Signer, uri string) (*activitystream.Object, error) {
	resp, err := signer.GetWithSign(ctx, uri)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("returned %v", resp.Status)
	}
	obj := &activitystream.Object{}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxRemoteBody)).Decode(obj); err != nil {
		return nil, err
	}
	return obj, nil
}
