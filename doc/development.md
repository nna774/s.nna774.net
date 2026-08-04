# ローカル開発

## 前提条件

- Go 1.18 以上
- Docker（DynamoDB Local 用）
- make
- AWS CLI（`aws` コマンド）

## セットアップ

### 1. リポジトリをクローン

```sh
git clone https://github.com/nna774/s.nna774.net.git
cd s.nna774.net
```

### 2. DynamoDB Local を起動

```sh
make dynamodb-local
```

これで `localhost:8000` で DynamoDB Local が起動する。

### 3. ローカルテーブルを作成

```sh
make local-table
```

本番と同じキースキーマで2つのテーブルが作成される：
- `s-nna774-net`（連番テーブル）
- `s-nna774-net-kv`（KV テーブル）

### 4. 環境変数を設定

```sh
# 開発モードの場合、以下の環境変数を設定
export ENV=development
export API_TOKEN="test-token-12345"        # テスト用トークン
export SESSION_SECRET="test-secret-67890"  # Cookie 署名用鍵
```

**自動読み込み**: `ENV=development` 時、これらは `.env` ファイルから読み込まれることも可能。

### 5. サーバを起動

```sh
make dev
```

`localhost:8080` でサーバが起動する。

## テスト

### 単体テスト

```sh
make test
```

全パッケージのテストが走る。覆率を確認する場合：

```sh
go test -cover ./...
```

### テスト対象

- `actor_test.go` - Actor エンドポイント
- `inbox_test.go` - 受信フロー
- `mention_test.go` - メンション処理
- `notification_test.go` - 通知管理
- `status_test.go`（存在する場合）- ステータス操作

## ビルド

### ローカルビルド

```sh
make build
```

`build/bootstrap` に Go バイナリが生成される。

### デバッグビルド

```sh
go build -o build/bootstrap ./cmd/...
```

または

```sh
go build -o build/bootstrap .
```

## リント

```sh
make lint
```

Go Vet と Golangci-lint でコードをチェックする。

## 開発環境の動作

### 開発モード (`ENV=development`)

- 秘密鍵をファイルシステムから読み込む（`private.key`）
- トークンは環境変数から読み込む（`API_TOKEN` / `SESSION_SECRET`）
- **SSM Parameter Store にアクセスしない**
- Cookie の `Secure` 属性が外れる（localhost は HTTPS でないため）
- DynamoDB Local に接続

### 署名鍵の生成

```sh
make keys
```

`private.key` が生成される。公開鍵は自動導出されるため別ファイルは不要。

### 既存の秘密鍵を確認

```sh
make pubkey
```

Actor に載る publicKeyPem を表示。

## デバッグ

### ログ出力

標準出力に出力されたログは CloudWatch Logs に相当する形式で流れる。Lambda 実行時は自動的に CloudWatch Logs に送信される。

### リクエストのテスト

curl でテスト：

```sh
# ローカルの投稿一覧を取得
curl http://localhost:8080/u/nana/outbox

# HTML ティムラインを見る
curl -H "Authorization: Bearer test-token-12345" http://localhost:8080/timeline

# JSON で返してもらう場合
curl -H "Accept: application/json" http://localhost:8080/u/nana
```

### 署名付きリクエストのテスト

```sh
go run ./tools/apclient -get http://localhost:8080/u/nana
```

## データの初期化

### DynamoDB Local をリセット

```sh
# Docker コンテナを削除（DynamoDB Local）
docker stop $(docker ps | grep dynamodb-local | awk '{print $1}')
docker rm $(docker ps -a | grep dynamodb-local | awk '{print $1}')

# 再起動
make dynamodb-local
make local-table
```

### すべてのテーブルを削除してやり直す

```sh
aws dynamodb list-tables --endpoint-url http://localhost:8000 --region ap-northeast-1 \
  | jq -r '.TableNames[]' \
  | xargs -I {} aws dynamodb delete-table --table-name {} --endpoint-url http://localhost:8000 --region ap-northeast-1
```

その後 `make local-table` で再作成。

## よくある問題

### DynamoDB Local に接続できない

```
error connecting to DynamoDB
```

確認:

```sh
# DynamoDB Local が起動しているか
docker ps | grep dynamodb
```

起動していなければ `make dynamodb-local` を実行。

### テーブルが見つからない

```
ResourceNotFoundException: Requested resource not found
```

確認:

```sh
aws dynamodb list-tables --endpoint-url http://localhost:8000
```

テーブルがなければ `make local-table` を実行。

### ポート競合

```
bind: address already in use
```

既に別のプロセスが `localhost:8080` を使用。

```sh
lsof -i :8080
kill -9 <PID>
```

その後サーバを再起動。

## 継続的な開発

### ファイル監視でホットリロード

```sh
# ファイル更新を監視して再ビルド・再起動（オプション）
# 標準で用意がないため、別途 entr などのツールが必要
go install github.com/cosmtrek/air@latest
air
```

`.air.toml` で設定可能。

### 本番環境との同期

秘密情報の取得:

```sh
# AWS 資格情報が必要
aws ssm get-parameter --name /s.nna774.net/private-key --region ap-northeast-1 \
  --with-decryption --query 'Parameter.Value'
```

開発環境ではこれを `private.key` に保存すれば OK。
