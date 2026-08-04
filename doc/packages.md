# パッケージ構成

## パッケージ一覧

| パッケージ | ファイル | 責務 |
|---|---|---|
| `activitystream` | `activitystream/` | ActivityStreams の型定義・シリアライズ。`Ref` が「文字列 URI または埋め込みオブジェクト」を統一的に扱う |
| `httpsigclient` | `httpsigclient/` | HTTP Signature の署名と検証。`go-fed/httpsig` をラップ |
| `datastore` | `datastore/` | DynamoDB アクセス。連番テーブルと KV テーブルの両操作を提供 |
| `auth` | `auth/` | 私用エンドポイントの認証。Bearer トークンと署名付き Cookie の検証 |
| `web` | `web/` | HTML テンプレートとリモート HTML のサニタイズ。タイムライン・ステータスページのレンダリング |
| `config` | `config/` | 設定と秘密情報の読み込み。環境変数と SSM Parameter Store からの取得 |
| `webfinger` | `webfinger/` | WebFinger エンドポイント。JRD 形式のレスポンス生成 |
| `httperror` | `httperror/` | エラーレスポンスのハンドラ受け皿。JSON / HTML 形式の統一的なエラー返却 |

## ハンドラ（main.go と個別ハンドラファイル）

| ハンドラ | ファイル | 責務 |
|---|---|---|
| ルーティング・公開エンドポイント | `main.go` | API Gateway の全エンドポイントを定義。`/u/:user`・`/.well-known/` など |
| 受信・Federation | `inbox.go` | `POST /u/:user/inbox` 処理。Activity 検証・受信処理・Accept 送信 |
| 投稿 | `status.go` | 投稿作成・削除・フェッチ。タイムラインへの追加・配信 |
| フォロー管理 | `follower.go` | フォロー・フォロー解除。フォロワー情報の永続化・配信 |
| 配信 | `delivery.go` | 署名付きリクエストの送信。リモート Inbox への Activity 配信 |
| HTML 生成 | `page.go` | HTML ステータスページ・タイムラインのレンダリング |
| 通知 | `notification.go` | いいね・ブースト・返信・フォロー通知の管理 |
| Actor エンドポイント | `actor.go` | `GET /u/:user` の JSON / HTML 出し分け。Person オブジェクト生成 |
| well-known | `wellknown.go` | `/.well-known/` 配下のエンドポイント。webfinger・host-meta・nodeinfo |
| 私用エンドポイント | `private.go` | 認証が必須な全エンドポイント。`/timeline`・投稿・削除など |

## 外部依存

- **`go-fed/httpsig`**: HTTP Signature 検証の基盤
- **AWS SDK for Go**: DynamoDB・SSM・Lambda のアクセス
- **標準ライブラリ**: `net/http`・`encoding/json`・`crypto/*` など

## データ構造

### ActivityStreams 型（activitystream パッケージ）

```go
// Object: Create・Note・Article など、複数のオブジェクト型を表現
type Object struct {
    // 共通フィールド
    ID        string
    Type      string
    Published time.Time
    // ...
    
    // 型ごとの値を interface{} で保持
    // MarshalJSON で分岐してシリアライズ
}

// Ref: 文字列 URI または埋め込みオブジェクト
type Ref struct {
    String string    // URI の場合
    Object *Object   // オブジェクトの場合
}

// OrderedCollection: ページング対応のコレクション
type OrderedCollection struct {
    ID           string
    Type         string
    TotalItems   int
    First        string // ページングの URL
}
```

### DynamoDB キースキーマ

**テーブル `s-nna774-net`** (連番管理)

```
PK (Partition Key): kind (String)
  例) "status" / "outbox" / "timeline" / "notification" / "counter"
SK (Sort Key): id (Number)
  連番の ID（タイムスタンプベース）
```

**テーブル `s-nna774-net-kv`** (URI 参照)

```
PK (Partition Key): uri (String)
  例) "https://s.nna774.net/u/nana/status/42"
    / "https://mastodon.social/users/x"
    / "follow:https://mastodon.social/users/x"
Attributes: JSON ドキュメント形式
```

## 開発時の注意

### パッケージ追加時

新しいパッケージを追加する場合、この表に記載して責務を明記すること。責務が複数に跨る場合は分割を検討する。

### テスト

各ハンドラとパッケージは対応する `_test.go` ファイルで単体テスト。運用テストは `tools/apclient` を使う。

### インポート循環

- `main.go` が各ハンドラをインポート
- ハンドラは `datastore`・`auth`・`config` などのパッケージをインポート
- パッケージ間は依存関係を最小化（循環参照を避ける）
