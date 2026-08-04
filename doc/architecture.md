# アーキテクチャ

## システム構成図

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
        │   (投稿・outbox・タイムライン・通知・カウンタ)
        │
        ├── DynamoDB s-nna774-net-kv   … URI で引くもの
        │   (フォロワー・公開鍵と表示名のキャッシュ・重複排除・いいね・既読位置)
        │
        └── SSM Parameter Store        … 署名鍵・API トークン・Cookie 署名鍵
```

## デプロイ構成

### AWS Lambda

- **Runtime**: `provided.al2023` (カスタムランタイム)
- **Architecture**: arm64
- **Bootstrap**: 静的にコンパイルされた Go バイナリ (`build/bootstrap`)
- **Timeout**: 30 秒（デフォルト）
- **Memory**: 状況に応じて設定（通常 128 MB 以上推奨）

**重要**: `CodeUri: build/` を外してはならない。省略するとリポジトリ全体がビルド成果物に含まれ、秘密鍵・ソース・`.git` まで Lambda に同梱される。`build/` には `bootstrap` と `config.yml` だけを置く。

### DynamoDB テーブル

| テーブル名 | 用途 | キースキーマ | ソート | 説明 |
|---|---|---|---|---|
| `s-nna774-net` | 連番管理 | PK: kind | SK: id | 投稿・outbox・タイムライン・通知・カウンタなど、連番で管理する全データ |
| `s-nna774-net-kv` | KV ストア | PK: uri | - | フォロワー・公開鍵と表示名のキャッシュ・重複排除・いいね・既読位置などの参照用 |

**スケーリング**: オンデマンド課金で、プロビジョンド（1 RCU / 1 WCU）と異なり、トラフィック変動に自動対応。1人用で事実上ゼロトラフィックなため、月額コストは一桁円程度。

### SSM Parameter Store

秘密情報は**標準階層**で **AWS 管理キー** (`aws/ssm`) を使用。カスタマー管理キーは不要（月額コスト発生）。

| パラメータ | 用途 | 形式 |
|---|---|---|
| `/s.nna774.net/private-key` | HTTP Signature の署名鍵 | RSA 2048 PKCS#1 または PKCS#8 |
| `/s.nna774.net/api-token` | 私用エンドポイントの資格情報 | ランダムな Base64 文字列 |
| `/s.nna774.net/session-secret` | Cookie 署名用の HMAC 鍵 | ランダムな Base64 文字列 |

### CloudWatch Logs

Lambda の標準出力は自動で `/aws/lambda/s-nna774-net` に記録される。リテンション期間は**30 日**に設定済み（ログ肥大化防止）。

## 通信フロー

### 受信系（ActivityPub Federation）

1. リモートサーバーから Inbox (`POST /u/:user/inbox`) に Activity が送られてくる
2. HTTP Signature で検証（`go-fed/httpsig` ベース）
3. Activity の種類に応じて処理（Follow・Create・Delete など）
4. Accept 応答を同期で送信

### 送信系（Federation）

1. ローカルで Activity を生成（Create・Follow・Accept など）
2. 宛先のアクターを収集（フォロワー・返信先・メンション）
3. HTTP Signature で署名してリモート Inbox に POST
4. 配信は同期（Lambda 30 秒以内に完了）

## セキュリティ

### HTTP Signature

- 署名方式: RSA-SHA256
- 署名対象: `(request-target)`, `host`, `date`, `digest`
- 検証: `keyId` の所有者と `actor` の一致を確認
- Digest: 本文のハッシュを検証（改竄防止）

### ブラウザ認証

- **Cookie**: `HMAC-SHA256(session-secret, 有効期限)` で自己検証型
- **属性**: `HttpOnly; Secure; SameSite=Strict`
- **CSRF 対策**: `SameSite=Strict` + `Sec-Fetch-Site: same-origin` 検証

### API 認証

- **Bearer トークン**: `Authorization: Bearer <token>`
- **保存先**: SSM の `api-token`

## 設計上の重要な判断

- **Activity Streams 型は自作**: `go-fed/activity` への移行は全面書き直し
- **単一平坦な `Object` 構造**: 型ごとに `interface{}` で値を持ち、`MarshalJSON` で分岐
- **`Ref` 型で文字列 URI と埋め込みオブジェクト両対応**: ActivityPub では両形態が混在
- **配信は同期**: Lambda 30 秒で完了できるため
- **セッションストア不要**: Cookie で自己検証できるため
