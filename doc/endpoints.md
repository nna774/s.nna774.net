# エンドポイント一覧

## 公開エンドポイント（認証不要）

連合が依存するため、ここに認証を掛けると**フェディバースから静かに消える**。`inbox` は HTTP Signature という別系統で守られている。

### Actor・Collection

| メソッド | パス | 説明 | 戻り値 |
|---|---|---|---|
| `GET` | `/u/:user` | Actor オブジェクト | JSON / HTML (`Accept` で出し分け) |
| `GET` | `/u/:user/outbox` | OrderedCollection (全投稿一覧) | JSON |
| `GET` | `/u/:user/outbox/page` | ページング済み outbox | JSON (`.since_id` / `.until_id` で指定) |
| `GET` | `/u/:user/followers` | Followers コレクション | JSON |
| `GET` | `/u/:user/following` | Following コレクション | JSON |

### ステータス・投稿

| メソッド | パス | 説明 | 戻り値 |
|---|---|---|---|
| `GET` | `/u/:user/status` | 投稿一覧 (HTML) | HTML (`.page=n` で古い方へ遡る) |
| `GET` | `/u/:user/status/:id` | 個別投稿 | JSON / HTML (`Accept` で出し分け) |

### Federation

| メソッド | パス | 説明 | 戻り値 |
|---|---|---|---|
| `POST` | `/u/:user/inbox` | 受信エンドポイント | JSON (HTTP Signature 必須) |

### WebFinger・Well-Known

| メソッド | パス | 説明 | 戻り値 |
|---|---|---|---|
| `GET` | `/.well-known/webfinger` | WebFinger JRD | `application/jrd+json` |
| `GET` | `/.well-known/host-meta` | Host Meta (WebFinger 互換) | `application/xrd+xml` |
| `GET` | `/.well-known/nodeinfo` | NodeInfo 参照 | JSON |
| `GET` | `/.well-known/nodeinfo/2.1` | NodeInfo 2.1 | JSON |

### セッション・ログイン

| メソッド | パス | 説明 | 戻り値 |
|---|---|---|---|
| `GET` | `/login` | ログインフォーム | HTML |
| `POST` | `/login` | ログイン処理（トークンを Cookie に変換） | 302 リダイレクト + Set-Cookie |
| `POST` | `/logout` | ログアウト | 302 リダイレクト + Cookie 削除 |

**認証方式**: `POST /login` にトークン（クエリ `?token=...` または フォームボディ）を渡すと HMAC-SHA256 署名の Cookie が発行される。

## 私用エンドポイント（認証必須）

これらはブラウザかベアラートークンで保護されている。

### タイムライン・UI

| メソッド | パス | 説明 | 認証 |
|---|---|---|---|
| `GET` | `/timeline` | 受信タイムライン (投稿フォーム込み) | Bearer / Cookie |
| `GET` | `/notifications` | 通知一覧 (いいね・ブースト・返信・フォロー) | Bearer / Cookie |

### 投稿・削除

| メソッド | パス | 説明 | 認証 | リクエスト形式 |
|---|---|---|---|---|
| `POST` | `/u/:user/statuses` | 投稿作成 | Bearer / Cookie | JSON / form |
| `POST` | `/u/:user/statuses/:id/delete` | 削除 (form 用) | Cookie | form |
| `DELETE` | `/u/:user/status/:id` | 削除 (API 用) | Bearer | JSON |

**投稿パラメータ**:
```json
{
  "content": "やっぴー",
  "visibility": "public",           // "public" / "unlisted" / "followers"
  "in_reply_to": "https://...",     // (オプション) 返信先の投稿 URI
  "mentions": ["https://..."]       // (オプション) メンション対象のアクター URI
}
```

**画像添付**: `multipart/form-data` の `image` フィールドに画像を乗せると、
Gyazo にアップロードした上で Note の `attachment` に載せる。JSON リクエスト
にファイルを乗せる方法は無いので form 専用。`gyazo_access_token_parameter`
が設定されていない場合はエラーになる。

### フォロー管理

| メソッド | パス | 説明 | 認証 | リクエスト |
|---|---|---|---|---|
| `POST` | `/u/:user/following` | フォロー追加 | Bearer / Cookie | `{"actor":"https://..."}` |
| `DELETE` | `/u/:user/following?actor=...` | フォロー削除 | Bearer / Cookie | クエリパラメータ |

## レスポンス形式

### JSON (API)

全エンドポイントは適切な HTTP ステータスコードと JSON レスポンスを返す。

```json
{
  "id": "https://s.nna774.net/u/nana/status/42",
  "type": "Note",
  "content": "投稿内容",
  "published": "2023-12-01T12:34:56Z",
  "attributedTo": "https://s.nna774.net/u/nana",
  "inReplyTo": "https://...",
  "attachment": [
    {
      "type": "Image",
      "mediaType": "image/jpeg",
      "url": "https://..."
    }
  ]
}
```

### HTML (Web UI)

`Accept: text/html` でアクセスすると HTML を返す。

- `/u/:user` → Person の HTML 表示
- `/u/:user/status/:id` → 個別投稿ページ
- `/u/:user/status` → タイムライン (ページネーション付き)
- `/timeline` → 投稿フォーム付きタイムライン (認証済み)
- `/notifications` → 通知一覧

## エラーハンドリ

エラーは適切な HTTP ステータスと JSON を返す。

```json
{
  "error": "unauthorized",
  "message": "Invalid token"
}
```

| ステータス | 原因 |
|---|---|
| `400 Bad Request` | リクエスト形式の不正 |
| `401 Unauthorized` | 認証失敗 |
| `404 Not Found` | リソースが見つからない |
| `405 Method Not Allowed` | HTTP メソッドが不許可 |
| `500 Internal Server Error` | サーバー内部エラー |

## curl での利用例

```sh
# 投稿
curl -X POST https://s.nna774.net/u/nana/statuses \
  -H "Authorization: Bearer $TOKEN" \
  -H 'content-type: application/json' \
  -d '{"content":"やっぴー","visibility":"public"}'

# 返信 + メンション
curl -X POST https://s.nna774.net/u/nana/statuses \
  -H "Authorization: Bearer $TOKEN" \
  -H 'content-type: application/json' \
  -d '{"content":"そうだな",
       "in_reply_to":"https://pawoo.net/users/x/statuses/1",
       "mentions":["https://pawoo.net/users/x"]}'

# フォローする
curl -X POST https://s.nna774.net/u/nana/following \
  -H "Authorization: Bearer $TOKEN" \
  -H 'content-type: application/json' \
  -d '{"actor":"https://pawoo.net/users/kugayama"}'

# 削除
curl -X DELETE "https://s.nna774.net/u/nana/status/42" \
  -H "Authorization: Bearer $TOKEN"
```

## 署名付きリクエスト

Federation の動作確認には `tools/apclient` を使う。curl では HTTP Signature が作成できない。

```sh
go run ./tools/apclient -post https://example.com/users/x/inbox -body follow.json
go run ./tools/apclient -get https://pawoo.net/users/kugayama
```
