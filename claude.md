# s.nna774.net プロジェクト用 claude.md

## プロジェクト概要

1人用の ActivityPub サーバ。Go + AWS Lambda + DynamoDB で動作。

```
@nana@s.nna774.net
```

**リポジトリ**: https://github.com/nna774/s.nna774.net

## クイックリファレンス

### ローカル開発

```sh
make dynamodb-local   # DynamoDB Local を起動（別ターミナル）
make local-table      # テーブルを作成
make dev              # localhost:8080 で起動
make test             # テスト実行
make lint             # lint 実行
```

### ビルド・デプロイ

```sh
make build            # ローカルビルド（build/bootstrap を生成）
make deploy           # AWS SAM でデプロイ
make keys             # 秘密鍵を生成（開発時）
make put-key          # SSM に秘密鍵を登録
make pubkey           # Actor に載る公開鍵を確認
```

### 環境変数（開発時 `ENV=development`）

```sh
export ENV=development
export API_TOKEN="test-token-12345"        # テスト用トークン
export SESSION_SECRET="test-secret-67890"  # Cookie 署名用鍵
```

## ディレクトリ構造

```
.
├── README.md              # プロジェクト概要
├── doc/                   # ドキュメント目次
│   ├── README.md
│   ├── architecture.md
│   ├── packages.md
│   ├── endpoints.md
│   ├── development.md
│   ├── deployment.md
│   └── design-decisions.md
├── activitystream/        # ActivityStreams 型定義
├── httpsigclient/         # HTTP Signature クライアント
├── datastore/             # DynamoDB アクセス
├── auth/                  # 認証（Bearer・Cookie）
├── web/                   # HTML テンプレート・サニタイズ
├── config/                # 設定・秘密情報読み込み
├── webfinger/             # WebFinger エンドポイント
├── httperror/             # エラーハンドラ
├── tools/apclient         # 署名付きリクエストのテスト用
├── main.go                # ルーティング・公開エンドポイント
├── inbox.go               # 受信フロー
├── status.go              # 投稿操作
├── follower.go            # フォロー管理
├── delivery.go            # リモート配信
├── page.go                # HTML 生成
├── notification.go        # 通知管理
├── actor.go               # Actor エンドポイント
├── wellknown.go           # well-known エンドポイント
├── private.go             # 私用エンドポイント
├── Makefile               # ビルド・デプロイコマンド
├── template.yaml          # AWS SAM テンプレート
└── config.yml             # AWS Lambda 用設定
```

## 重要なファイル

### Makefile

| ターゲット | 用途 |
|---|---|
| `make dynamodb-local` | DynamoDB Local を Docker で起動 |
| `make local-table` | ローカルテーブルを作成 |
| `make dev` | localhost:8080 で起動 |
| `make test` | テスト実行 |
| `make lint` | lint 実行 |
| `make build` | ビルド（build/bootstrap を生成） |
| `make deploy` | AWS SAM でデプロイ |
| `make keys` | 秘密鍵生成 |
| `make put-key` | SSM に秘密鍵登録 |
| `make pubkey` | 公開鍵確認 |

### template.yaml

AWS SAM テンプレート。以下は変更禁止：
- **`CodeUri: build/`**: リポジトリ全体が同梱されるのを防ぐ
- **`Runtime: provided.al2023`**
- **`Architectures: [arm64]`**

### config.yml

Lambda 実行時の設定。`build/config.yml` にコピーされてデプロイされる。

### go.mod / go.sum

依存パッケージ。更新時は `go mod tidy` を実行。

## エンドポイント

### 公開（認証不要）

- `GET /u/:user` - Actor（JSON / HTML）
- `POST /u/:user/inbox` - 受信（HTTP Signature 必須）
- `GET /u/:user/outbox` - OrderedCollection
- `GET /u/:user/status/:id` - 個別投稿（JSON / HTML）
- `GET /.well-known/webfinger` - WebFinger
- `POST /login` - ログイン
- その他 `/.well-known/` エンドポイント

### 私用（認証必須）

- `GET /timeline` - タイムライン（投稿フォーム付き）
- `GET /notifications` - 通知
- `POST /u/:user/statuses` - 投稿作成
- `DELETE /u/:user/status/:id` - 投稿削除
- `POST /u/:user/following` - フォロー追加
- `DELETE /u/:user/following?actor=...` - フォロー削除

詳細は `doc/endpoints.md` を参照。

## 秘密情報（AWS SSM Parameter Store）

**重要**: 標準階層・AWS 管理キーを使う。カスタマー管理キーは不要（月額コスト）。

| パラメータ | 用途 |
|---|---|
| `/s.nna774.net/private-key` | HTTP Signature の署名鍵（RSA 2048） |
| `/s.nna774.net/api-token` | API トークン |
| `/s.nna774.net/session-secret` | Cookie 署名鍵 |
| `/s.nna774.net/gyazo-access-token` | 画像投稿用の Gyazo アクセストークン |

## DynamoDB テーブル

| テーブル | キースキーマ | 用途 |
|---|---|---|
| `s-nna774-net` | PK: kind, SK: id | 連番（投稿・outbox・通知・カウンタ） |
| `s-nna774-net-kv` | PK: uri | KV ストア（フォロワー・キャッシュ・重複排除） |

**スケーリング**: オンデマンド課金。1人用で月額数円程度。

## 設計上の重要な判断

1. **Activity Streams 型は自作**：`go-fed/activity` への移行なし
2. **`Object` は単一平坦構造**：`interface{}` で型を吸収
3. **`Ref` で文字列 URI と埋め込みオブジェクト両対応**
4. **公開鍵は秘密鍵から導出**：鍵の不整合を防ぐ
5. **Digest と本文は自前照合**：改竄防止
6. **`keyId` と `actor` の一致確認**：なりすまし防止
7. **配信は同期**：Lambda 30 秒以内に完了
8. **テーブルスキーマは変更なし**：2023 年からのデータ保護
9. **タイムラインに JS なし**：素の form と 303 リダイレクト
10. **Accept は goroutine で送らない**：Lambda 環境の制限

詳細は `doc/design-decisions.md` を参照。

## トラブルシューティング

### DynamoDB に接続できない

```sh
# DynamoDB Local が起動しているか確認
docker ps | grep dynamodb

# 起動していなければ
make dynamodb-local
```

### テーブルが見つからない

```sh
# テーブル一覧を確認
aws dynamodb list-tables --endpoint-url http://localhost:8000

# なければ作成
make local-table
```

### ポート競合

```sh
# 8080 を使っているプロセスを確認・停止
lsof -i :8080
kill -9 <PID>
```

### リント・テスト失敗

```sh
# 依存パッケージを更新
go mod tidy
go mod download

# 再実行
make lint
make test
```

## 開発ワークフロー

1. **ブランチ作成**: `git checkout -b feature/xxx`
2. **ローカルで開発・テスト**
   - `make dev` でサーバ起動
   - `make test` でテスト実行
   - `make lint` でチェック
3. **コミット**: 意味単位でコミット（大きすぎるコミットは避ける）
4. **Push**: `git push origin feature/xxx`
5. **PR 作成**: GitHub で PR を作成
6. **マージ**: main に merge 後、`git fetch` で最新の状態に更新

## コミット規約

- 日本語で書く
- Rebase は基本的に行わず、master を merge する形で進める
- 意味単位で適宜コミット（大きすぎるコミットは避ける）
- セッション ID は自動付与される（`Co-Authored-By: Claude ...`）

## デプロイ

```sh
make deploy
```

自動的に以下が実行される：
1. スタティックビルド（arm64 用）
2. `build/bootstrap` に配置
3. AWS SAM でデプロイ

**注意**: `CodeUri: build/` を外してはならない。

## ドキュメント

- `doc/README.md` - ドキュメント目次
- `doc/architecture.md` - システムアーキテクチャ
- `doc/packages.md` - パッケージ構成
- `doc/endpoints.md` - エンドポイント一覧
- `doc/development.md` - ローカル開発の手順
- `doc/deployment.md` - デプロイ手順
- `doc/design-decisions.md` - 設計上の判断

## よく使うコマンド

```sh
# 開発サーバを起動
make dev

# テスト実行
make test

# テストカバレッジを確認
go test -cover ./...

# ビルド
make build

# デプロイ
make deploy

# 秘密鍵を生成
make keys

# 秘密鍵を SSM に登録
make put-key

# 公開鍵を確認
make pubkey

# curl でテスト
curl http://localhost:8080/u/nana

# 署名付きリクエストのテスト
go run ./tools/apclient -get http://localhost:8080/u/nana
```

## 外部リンク

- **GitHub**: https://github.com/nna774/s.nna774.net
- **ActivityPub Academy**: https://activitypub.academy
- **go-fed**: https://github.com/go-fed
- **AWS SAM**: https://aws.amazon.com/serverless/sam/

## 参考文献

- ActivityPub Spec: https://www.w3.org/TR/activitypub/
- ActivityStreams Spec: https://www.w3.org/TR/activitystreams-core/
- HTTP Signature: https://tools.ietf.org/html/draft-cavage-http-signatures
