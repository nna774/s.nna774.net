# s.nna774.net

1人用の ActivityPub サーバ。Go + AWS Lambda + DynamoDB で動く。

`@nana@s.nna774.net`

## ドキュメント

詳細情報は `doc/` 以下をご覧ください。

- [ドキュメント目次](doc/README.md) — 全体のナビゲーション
- [アーキテクチャ](doc/architecture.md) — システム構成・DynamoDB・セキュリティ
- [パッケージ構成](doc/packages.md) — 各パッケージの責務
- [エンドポイント](doc/endpoints.md) — 全エンドポイント一覧・curl 例
- [ローカル開発](doc/development.md) — 環境構築・テスト・デバッグ
- [デプロイ](doc/deployment.md) — 本番環境への展開・秘密情報管理
- [設計上の判断](doc/design-decisions.md) — 10の重要な設計判断と理由

## 構成

```
ブラウザ / Mastodon など
        │
        ▼
API Gateway (s.nna774.net)
        │
        ▼
Lambda (provided.al2023, arm64, bootstrap)
        │
        ├── DynamoDB s-nna774-net      … 連番で持つもの
        ├── DynamoDB s-nna774-net-kv   … URI で引くもの
        └── SSM Parameter Store        … 署名鍵・トークン・Cookie 鍵
```

詳細は [アーキテクチャ](doc/architecture.md) を参照。

| パッケージ | 役割 |
|---|---|
| `activitystream` | ActivityStreams 型定義 |
| `httpsigclient` | HTTP Signature 署名・検証 |
| `datastore` | DynamoDB アクセス |
| `auth` | Bearer・Cookie 認証 |
| `web` | HTML テンプレート・サニタイズ |
| `config` | 設定・秘密情報読み込み |
| `webfinger` / `httperror` | WebFinger・エラーハンドラ |

詳細は [パッケージ構成](doc/packages.md) を参照。

## エンドポイント

全エンドポイント・curl 例・レスポンス形式は [エンドポイント](doc/endpoints.md) を参照。

## 認証

秘密は SSM に置いた共有シークレット1本で、受け口が2つ：

- **curl**: `Authorization: Bearer <token>`
- **ブラウザ**: `POST /login` にトークンを渡すと HMAC-SHA256 署名の Cookie が発行

詳細は [エンドポイント](doc/endpoints.md#認証) の認証セクションと
[アーキテクチャ](doc/architecture.md#セキュリティ) を参照。

## 使い方

```sh
# 投稿
curl -X POST https://s.nna774.net/u/nana/statuses \
  -H "Authorization: Bearer $TOKEN" \
  -H 'content-type: application/json' \
  -d '{"content":"やっぴー","visibility":"public"}'

# 削除
curl -X DELETE "https://s.nna774.net/u/nana/status/42" \
  -H "Authorization: Bearer $TOKEN"
```

より詳しい例は [エンドポイント](doc/endpoints.md#curl-での利用例) を参照。

## ローカル開発

```sh
make dynamodb-local   # DynamoDB Local を起動
make local-table      # テーブルを作成
make dev              # localhost:8080 で起動
make test             # テスト実行
make lint             # lint 実行
```

詳細は [ローカル開発](doc/development.md) を参照。

## デプロイ

```sh
make deploy
```

詳細なデプロイ手順・秘密情報管理・トラブルシューティングは
[デプロイ](doc/deployment.md) を参照。

## 設計上の判断

重要な設計判断と理由は [設計上の判断](doc/design-decisions.md) を参照。

概要：

- Activity Streams 型は自作
- `Object` は単一平坦構造体
- `Ref` で文字列 URI と埋め込みオブジェクト両対応
- 公開鍵は秘密鍵から導出
- Digest と本文は自前照合
- `keyId` と `actor` の一致確認
- 配信は同期
- テーブルスキーマは変更なし
- タイムラインに JS なし
- Accept は goroutine で送らない

## ライセンス

MIT
