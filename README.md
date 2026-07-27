# s.nna774.net

1人用の ActivityPub サーバ。Go + AWS Lambda + DynamoDB で動く。

`@nana@s.nna774.net`

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
        ├── DynamoDB s-nna774-net      … 連番で持つもの (投稿・outbox・タイムライン・カウンタ)
        ├── DynamoDB s-nna774-net-kv   … URI で引くもの (フォロワー・公開鍵キャッシュ・重複排除)
        └── SSM Parameter Store        … 署名鍵・API トークン・Cookie 署名鍵
```

| パッケージ | 役割 |
|---|---|
| `activitystream` | ActivityStreams の型。`Ref` が「文字列 URI または埋め込みオブジェクト」を吸収する |
| `httpsigclient` | HTTP Signature の署名と検証 |
| `datastore` | DynamoDB。連番テーブルと KV テーブルの2本 |
| `auth` | 私用エンドポイントの認証 (Bearer と署名付き Cookie) |
| `web` | HTML テンプレートとリモート HTML のサニタイズ |
| `config` | 設定と秘密情報の読み込み |
| `webfinger` / `httperror` | WebFinger の JRD / エラーを返すハンドラの受け皿 |

ハンドラは `main.go` (ルーティングと公開エンドポイント)、`inbox.go` (受信)、
`status.go` (投稿)、`follower.go`、`delivery.go`、`page.go` (HTML)、
`actor.go`、`wellknown.go`、`private.go` に分かれている。

## エンドポイント

### 公開 — 認証を掛けてはならない

連合が依存するため、ここに認証を掛けると**フェディバースから静かに消える**。
`inbox` は HTTP Signature という別系統で守られている。

| | |
|---|---|
| `GET /u/:user` | actor (`Accept` で JSON / HTML を出し分け) |
| `POST /u/:user/inbox` | 受信。HTTP Signature 必須 |
| `GET /u/:user/outbox` | `OrderedCollection` |
| `GET /u/:user/outbox/page` | `since_id` / `until_id` でページング |
| `GET /u/:user/status/:id` | 個別投稿 (JSON / HTML) |
| `GET /u/:user/followers`, `/following` | コレクション |
| `GET /.well-known/webfinger` | `application/jrd+json` |
| `GET /.well-known/host-meta` | `application/xrd+xml` |
| `GET /.well-known/nodeinfo`, `/nodeinfo/2.1` | nodeinfo 2.1 |
| `GET /login`, `POST /login`, `POST /logout` | ログイン |

### 私用 — 認証必須

| | |
|---|---|
| `GET /timeline` | 受信タイムライン (投稿フォーム込み) |
| `POST /u/:user/statuses` | 投稿 (JSON / form) |
| `POST /u/:user/statuses/:id/delete` | 削除 (form 用) |
| `DELETE /u/:user/status/:id` | 削除 (API 用) |
| `POST /u/:user/following` | フォローする |
| `DELETE /u/:user/following?actor=…` | フォロー解除 |

## 認証

秘密は SSM に置いた共有シークレット1本 (`api-token`) で、受け口が2つある。

- **curl**: `Authorization: Bearer <token>`
- **ブラウザ**: `POST /login` にトークンを渡すと `HMAC-SHA256(session-secret, 有効期限)`
  を焼いた Cookie が発行される (`HttpOnly; Secure; SameSite=Strict`)

Cookie は自己検証できるのでセッションストアを持たない。DynamoDB を1回も引かない。
全セッションを失効させたいときは `session-secret` を差し替えれば良い。
`api-token` は据え置ける。

CSRF 対策は `SameSite=Strict` と、状態変更時の `Sec-Fetch-Site: same-origin`
検証の二重。後者はブラウザが強制付与し JS から偽装できない。Bearer 経路には
掛けない (ブラウザが自動付与しないため CSRF が成立しない)。

## 使い方

```sh
# 投稿
curl -X POST https://s.nna774.net/u/nana/statuses \
  -H "Authorization: Bearer $TOKEN" -H 'content-type: application/json' \
  -d '{"content":"やっぴー","visibility":"public"}'

# 返信 + メンション
curl -X POST https://s.nna774.net/u/nana/statuses \
  -H "Authorization: Bearer $TOKEN" -H 'content-type: application/json' \
  -d '{"content":"そうだな","in_reply_to":"https://pawoo.net/users/x/statuses/1",
       "mentions":["https://pawoo.net/users/x"]}'

# フォローする
curl -X POST https://s.nna774.net/u/nana/following \
  -H "Authorization: Bearer $TOKEN" -H 'content-type: application/json' \
  -d '{"actor":"https://pawoo.net/users/kugayama"}'

# 削除
curl -X DELETE "https://s.nna774.net/u/nana/status/42" -H "Authorization: Bearer $TOKEN"
```

`visibility` は `public` / `unlisted` / `followers`。
ブラウザからは `https://s.nna774.net/timeline` の投稿フォームでも同じことができる。

### 署名付きリクエストを手で投げる

連合の動作確認には `tools/apclient` を使う。curl では署名を作れない。

```sh
go run ./tools/apclient -post https://example.com/users/x/inbox -body follow.json
go run ./tools/apclient -get https://pawoo.net/users/kugayama
```

## デプロイ

```sh
make deploy
```

`provided.al2023` / arm64 / `bootstrap` でビルドし、SAM でデプロイする。

**`CodeUri: build/` を外してはならない。** 省略すると SAM がリポジトリ全体を
zip し、秘密鍵・ソース・`.git` まで Lambda に同梱される。`lambda:GetFunction`
の権限があれば誰でもそれを取得できる。`build/` には `bootstrap` と
`config.yml` だけを置く。

### 秘密情報

SSM Parameter Store の SecureString で持つ。**標準階層と AWS 管理キー
(`aws/ssm`) を使う**こと。カスタマー管理キーを作ると 1 本 1 ドル/月かかる。
標準階層なら保存料・API 料・KMS 月額のいずれも発生しない。

| パラメータ | 用途 |
|---|---|
| `/s.nna774.net/private-key` | HTTP Signature の署名鍵 (RSA 2048) |
| `/s.nna774.net/api-token` | 私用エンドポイントの資格情報 |
| `/s.nna774.net/session-secret` | Cookie 署名用の HMAC 鍵 |

```sh
make keys      # private.key を作る (公開鍵はアプリが導出するので不要)
make put-key   # SSM に登録する
make pubkey    # actor に載る publicKeyPem を目視確認する

aws ssm put-parameter --region ap-northeast-1 \
  --name /s.nna774.net/api-token --type SecureString --tier Standard \
  --value "$(openssl rand -base64 32)"
aws ssm put-parameter --region ap-northeast-1 \
  --name /s.nna774.net/session-secret --type SecureString --tier Standard \
  --value "$(openssl rand -base64 32)"
```

秘密鍵は PKCS#1 でも PKCS#8 でも読める。OpenSSL 3 の `genrsa` は PKCS#8 を出す。

### 費用について

DynamoDB は**オンデマンド課金**にしてある。1人用でトラフィックが実質ゼロなので、
プロビジョンド (1 RCU / 1 WCU) の月 $0.65 よりずっと安く、書き込みの
スロットルも起きない。CloudWatch Logs は retention を切らないと際限なく
増えるので 30 日にしてある (ログ グループは Lambda が暗黙に作るため
スタック管理下に無く、CLI での操作になる)。

```sh
aws logs put-retention-policy --region ap-northeast-1 \
  --log-group-name /aws/lambda/s-nna774-net --retention-in-days 30
```

## ローカル開発

```sh
make dynamodb-local   # docker で dynamodb-local を起動
make local-table      # 本番と同じキースキーマでテーブルを2本作る
make dev              # localhost:8080 で起動
```

`ENV=development` のときは秘密鍵をファイルから読み、トークンは環境変数
(`API_TOKEN` / `SESSION_SECRET`) から取る。SSM を引かない。
Cookie の `Secure` 属性も外れる (localhost は HTTPS でないため)。

```sh
make test
make lint
```

## 設計上の判断

- **ActivityStreams の型は自作を維持する。** `go-fed/activity` への移行は
  ほぼ全面書き直しで、このリポジトリの趣旨を損なう。
- **`Object` は単一の平坦な構造体。** 型ごとの値を `interface{}` に持たせて
  `MarshalJSON` で分岐する方式は、型を足すたびに4箇所を揃える必要があり、
  1つ漏らすとフィールドが黙って消える。実際に `Person` と
  `OrderedCollection` で消えていた。
- **`Ref` 型で文字列 URI と埋め込みオブジェクトの両方を受ける。** ActivityPub
  では `object` / `actor` / `attributedTo` / `inReplyTo` がどちらの形でも来る。
  Mastodon が送る Follow の `object` は文字列 URI である。片方しか受けられないと
  誰にもフォローされない。
- **公開鍵は秘密鍵から導出する。** 別ファイルで持つと食い違ったままの公開鍵を
  actor に載せてしまい、リモートでの署名検証が通らなくなる余地が残る。
- **Digest と本文の照合は自前で行う。** `go-fed/httpsig` の Verifier は Digest
  ヘッダが署名対象に含まれていたことしか確認しない。これを省くと正しい署名を
  使い回して本文だけ差し替える改竄が通る。
- **`keyId` の所有者と `actor` の一致を確認する。** これを見ないと、自分の鍵で
  署名しつつ actor だけ他人を名乗れる。
- **配信は同期。** 宛先が少ないので Lambda の 30 秒に収まる。増えたら
  `deliver()` を SQS 経由に差し替える。
- **既存テーブルのキースキーマは変えない。** 2023 年からのデータを守るため、
  URI で引く用途には別テーブルを足した。
- **タイムラインに JS を書かない。** 素の form と 303 リダイレクトで完結する。
- **Accept は goroutine で送らない。** Lambda はレスポンス後に実行環境を
  凍結するので、大抵送信されないまま殺される。

## 相互運用性の確認

- `tools/apclient` で自分の inbox に署名付き Follow を投げると、署名検証・
  フォロワー永続化・Accept 配信までを第三者なしで通せる。
- [activitypub.academy](https://activitypub.academy) は使い捨ての Mastodon
  インスタンスで、送受信した生の Activity を見られる。Accept の JSON に元
  Follow の `id` が入っているかを直接確認できる。

## ライセンス

MIT
