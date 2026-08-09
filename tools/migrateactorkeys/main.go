// migrateactorkeys は複数アクター対応より前の nana (primary actor) の
// DynamoDB キーを、localpart プレフィックス付きの新しいキーへコピーする
// 一回限りの移行ツール。
//
// 対象:
//
//	メインテーブル: object--outbox / counter--outbox, object--status / counter--status
//	  -> object--<actor>:outbox / counter--<actor>:outbox,
//	     object--<actor>:status / counter--<actor>:status
//	KV テーブル: followers / following / mylikes / myboosts / likes / announced / myboostbyid
//	  -> <actor>:followers 等
//
// notification / actorkey / seen / actorinfo / cursor はローカルアクターに
// 依存しない共有リソースなので対象外 (doc/bot-account-design.md 参照)。
//
// 実行手順:
//
//  1. 新コード (このツールを含むブランチ) をデプロイする前に、旧コードが
//     稼働したまま以下を実行してコピーする (旧キーは残したまま新キーへ
//     コピーするだけなので、旧コードの動作に影響しない):
//
//     go run ./tools/migrateactorkeys -write
//
//  2. カウンタも含めて件数が一致していることを出力で確認する。
//
//  3. 新コードをデプロイする。
//
//  4. 新コードで一通り動作確認できたら、旧キーを削除する (このステップは
//     省略してもよい。旧キーが残っていても新コードは読みに行かないため
//     実害は無く、ストレージ費用も1人用インスタンスでは誤差):
//
//     go run ./tools/migrateactorkeys -delete-old
//
// 何度実行しても安全 (Put は上書き、カウンタは現在値からの差分だけ進める)。
//
//	go run ./tools/migrateactorkeys                          # 本番に対して dry-run
//	go run ./tools/migrateactorkeys -write                   # 新キーへコピー（旧キーは残す）
//	go run ./tools/migrateactorkeys -delete-old               # 旧キーを削除するだけ
//	go run ./tools/migrateactorkeys -endpoint http://localhost:8000 -write  # ローカル検証
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"

	"github.com/nna774/s.nna774.net/datastore"
)

// kvPartitions はアクター固有として移行する KV パーティション。
var kvPartitions = []string{
	datastore.KVFollowers,
	datastore.KVFollowing,
	datastore.KVMyLikes,
	datastore.KVMyBoosts,
	datastore.KVLikes,
	datastore.KVAnnounced,
	datastore.KVMyBoostByID,
}

func main() {
	region := flag.String("region", "ap-northeast-1", "AWS region")
	table := flag.String("table", "s-nna774-net", "DynamoDB table name")
	kvTable := flag.String("kv-table", "s-nna774-net-kv", "DynamoDB kv table name")
	endpoint := flag.String("endpoint", "", "DynamoDB endpoint override (dynamodb-local 検証用)")
	actor := flag.String("actor", "nana", "移行先のキーに使う localpart")
	write := flag.Bool("write", false, "新キーへ実際にコピーする。指定しなければ dry-run")
	deleteOld := flag.Bool("delete-old", false, "旧キーを削除する。新コードのデプロイと動作確認の後に単独で実行すること")
	flag.Parse()

	ctx := context.Background()
	client, err := datastore.NewClient(ctx, *region, *table, *kvTable, *endpoint)
	if err != nil {
		log.Fatalf("connecting to dynamodb: %v", err)
	}

	for _, name := range []string{"outbox", "status"} {
		if err := migrateObjectPartition(ctx, client, name, *actor, *write, *deleteOld); err != nil {
			log.Fatalf("migrating %s: %v", name, err)
		}
	}

	for _, pk := range kvPartitions {
		if err := migrateKVPartition(ctx, client, pk, *actor, *write, *deleteOld); err != nil {
			log.Fatalf("migrating kv %s: %v", pk, err)
		}
	}

	fmt.Printf("done (write=%v, delete-old=%v)\n", *write, *deleteOld)
}

// migrateObjectPartition は object と counter を name から "<actor>:name"
// へコピーする。カウンタは現在発行済みの最大 ID を表しており、単純に
// リセットすると次に発行する ID が既存の投稿と衝突しうるため、Inc を
// 差分回数だけ呼んで値を揃える (Client インタフェースにカウンタを直接
// 設定する手段が無いため)。
func migrateObjectPartition(ctx context.Context, client datastore.Client, name, actor string, write, deleteOld bool) error {
	newName := actor + ":" + name

	entries, err := client.TakeEntries(ctx, name, datastore.Inf, 1_000_000, datastore.Desc)
	if err != nil {
		return fmt.Errorf("listing %s: %w", name, err)
	}
	fmt.Printf("%s: %d objects to copy to %s\n", name, len(entries), newName)
	if write {
		for _, e := range entries {
			if err := client.Put(ctx, newName, e.ID, e.Object); err != nil {
				return fmt.Errorf("copying %s/%d: %w", name, e.ID, err)
			}
		}
	}

	top, err := client.Top(ctx, name)
	if err != nil && !errors.Is(err, datastore.ErrNotFound) {
		return fmt.Errorf("reading counter %s: %w", name, err)
	}
	fmt.Printf("%s: counter is %d\n", name, top)
	if write && top > 0 {
		newTop, err := client.Top(ctx, newName)
		if err != nil && !errors.Is(err, datastore.ErrNotFound) {
			return fmt.Errorf("reading counter %s: %w", newName, err)
		}
		if newTop < top {
			fmt.Printf("%s: advancing counter %s from %d to %d\n", name, newName, newTop, top)
		}
		for i := newTop; i < top; i++ {
			if _, err := client.Inc(ctx, newName); err != nil {
				return fmt.Errorf("advancing counter %s: %w", newName, err)
			}
		}
	}

	if deleteOld {
		for _, e := range entries {
			if err := client.DeleteObject(ctx, name, e.ID); err != nil {
				return fmt.Errorf("deleting old %s/%d: %w", name, e.ID, err)
			}
		}
		fmt.Printf("%s: deleted %d old objects (old counter itself is left in place; it is simply never read once nothing references %q anymore)\n", name, len(entries), name)
	}
	return nil
}

// migrateKVPartition は KV の1パーティションを pk から "<actor>:pk" へ
// コピーする。
func migrateKVPartition(ctx context.Context, client datastore.Client, pk, actor string, write, deleteOld bool) error {
	newPK := actor + ":" + pk
	items, err := client.QueryKV(ctx, pk)
	if err != nil {
		return fmt.Errorf("listing kv %s: %w", pk, err)
	}
	fmt.Printf("kv %s: %d items to copy to %s\n", pk, len(items), newPK)
	if write {
		for _, it := range items {
			moved := *it
			moved.PK = newPK
			if err := client.PutKV(ctx, &moved); err != nil {
				return fmt.Errorf("copying kv %s/%s: %w", pk, it.SK, err)
			}
		}
	}
	if deleteOld {
		for _, it := range items {
			if err := client.DeleteKV(ctx, pk, it.SK); err != nil {
				return fmt.Errorf("deleting old kv %s/%s: %w", pk, it.SK, err)
			}
		}
		fmt.Printf("kv %s: deleted %d old items\n", pk, len(items))
	}
	return nil
}
