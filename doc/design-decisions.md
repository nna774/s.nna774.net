# 設計上の判断

このドキュメントでは、プロジェクト全体に関わる重要な設計上の判断とその理由を説明する。

## Activity Streams 型は自作を維持する

### 判断

`go-fed/activity` への移行ではなく、`activitystream` パッケージで型を自作管理する。

### 理由

- `go-fed/activity` への全面書き直しになる
- このプロジェクトの趣旨（シンプルな1人用 ActivityPub サーバの実装例）を損なう
- 既存コードが安定していて、十分なカバレッジがある

### トレードオフ

**メリット**:
- プロジェクトの自立性が高い
- カスタマイズが容易（型の追加・削除が自由）

**デメリット**:
- ライブラリのバグフィックスの恩恵を受けられない
- 標準化から外れる可能性

### 影響範囲

- `activitystream/` パッケージの全体
- JSON マーシャリング・アンマーシャリング
- ActivityPub 仕様の更新追従は自前で行う

## `Object` は単一の平坦な構造体

### 判断

`Object` を複数の型に分けるのではなく、単一の構造体で全型を表現する。型ごとの値は `interface{}` フィールドに持たせ、`MarshalJSON` で分岐する。

### 背景

初期版では `interface{}` パターンを使い、型ごとに異なる構造体を持つ方式も試していたが、問題が発生した。

**問題点**:
- 新しい型を追加するたびに4箇所（フィールド・パーサー・マーシャラー・テスト）を揃える必要があった
- 1つでも漏らすと、フィールドが黙って消える（不可視の バグ）
- 実際に `Person` と `OrderedCollection` で消えていた

### 現在の方式の利点

```go
type Object struct {
    ID        string
    Type      string
    // 共通フィールド
    Attachment interface{} // 型ごとの個別フィールド
    Replies    interface{}
}

func (o *Object) MarshalJSON() []byte {
    // Type に応じて異なるフィールドを serialize
    switch o.Type {
    case "Note":
        // Note 固有のロジック
    case "Person":
        // Person 固有のロジック
    }
}
```

**メリット**:
- 新しい型は `interface{}` フィールドの値をセットするだけ
- フィールド漏れのリスクが低い
- 既存型の追加も容易

**デメリット**:
- 型安全性が弱い（`interface{}`）
- IDE のオートコンプリートが効きにくい

## `Ref` 型で文字列 URI と埋め込みオブジェクト両対応

### 判断

`Ref` という共通型で「文字列 URI」と「埋め込みオブジェクト」の両方を受けられるようにする。

### 必要性

ActivityPub では同じフィールド（`object`・`actor`・`attributedTo`・`inReplyTo`）が以下の形で来る：

```json
// 形式1：文字列 URI
{
  "type": "Follow",
  "object": "https://mastodon.social/users/foo"
}

// 形式2：埋め込みオブジェクト
{
  "type": "Follow",
  "object": {
    "id": "https://mastodon.social/users/foo",
    "type": "Person"
  }
}
```

**実例**: Mastodon が送る Follow の `object` は常に文字列 URI。片方しか受けられないと誰にもフォローされない。

### 実装

```go
type Ref struct {
    String string   // URI の場合
    Object *Object  // オブジェクトの場合
}

// JSON アンマーシャリング時に自動判定
func (r *Ref) UnmarshalJSON(data []byte) error {
    // JSON が文字列か オブジェクトかで判定
}
```

## 公開鍵は秘密鍵から導出する

### 判断

公開鍵をファイルやストレージに保存せず、秘密鍵から導出する。

### 理由

秘密鍵・公開鍵を別に管理すると、食い違ったままの鍵が公開される可能性がある。

**失敗シナリオ**:
1. 秘密鍵を更新
2. 公開鍵をうっかり更新し忘れ
3. Actor に古い公開鍵が載っている
4. リモートでの署名検証が失敗（「自分で署名した Activity が拒否される」という謎の問題）

### メリット

- 鍵の同期を気にせなくていい
- `publicKeyPem` は必要なときだけ導出

### 実装

```go
// private.key から公開鍵を導出
func DerivePublicKey(privateKey *rsa.PrivateKey) *rsa.PublicKey {
    return &privateKey.PublicKey
}
```

## Digest と本文の照合は自前で行う

### 判断

HTTP Signature の検証時に、Digest ヘッダが署名対象に含まれていることだけでなく、Digest と本文の内容を照合する。

### 背景

`go-fed/httpsig` の Verifier は以下を確認するだけ：
- 署名の形式が正しいか
- `digest` ヘッダが署名対象フィールドに含まれているか

**含まれていない確認**:
- Digest の値が実際に本文のハッシュと一致しているか

### 攻撃シナリオ

Digest チェックなしの場合：

```
1. 攻撃者が Follow リクエストを作成
2. オリジナルの本文と同じ Digest ハッシュを使う（別の本文で）
3. 正しい署名を使い回す
4. 本文だけ差し替える（例：フォロー対象を他人に変更）
5. リモート検証が通る（署名・Digest ともにチェック済みと見なされる）
```

### 実装

```go
// 署名検証後、さらに Digest と本文を照合
func VerifyDigest(body []byte, digestHeader string) bool {
    computed := sha256.Sum256(body)
    expected := parseDigestHeader(digestHeader)
    return computed == expected
}
```

## `keyId` の所有者と `actor` の一致確認

### 判断

HTTP Signature の `keyId` が指す公開鍵の所有者が、Activity の `actor` と一致することを確認する。

### 攻撃シナリオ

確認がない場合：

```
1. 攻撃者が自分の秘密鍵を使って署名
   keyId: "https://attacker.com/users/evil#main-key"
2. actor だけ本人に詐称
   actor: "https://target.com/users/victim"
3. 「victim が Follow を送った」と見えてしまう
```

### 実装

```go
// 署名を検証する際に keyId から actor を取得し、一致を確認
func VerifyActorConsistency(keyId string, actor string) bool {
    owner := ExtractActorFromKeyId(keyId)
    return owner == actor
}
```

## 配信は同期

### 判断

リモートへの Activity 配信（`deliver()` 関数）は同期処理で行う。非同期キュー（SQS など）は使わない。

### 理由

1. **宛先が少ない**: 1人用なので、フォロワーは数十人程度
2. **Lambda 30 秒制限内に完了**: 同期で十分に間に合う
3. **実装シンプル**: キューを管理する必要がない

### 拡張性

トラフィックが増えて 30 秒内に完了しなくなった場合：

```go
// deliver() を SQS 経由に差し替える
func deliver(activity *Object) {
    // 現在：同期で送信
    httpPost(remoteInbox, activity)
    
    // 拡張時：SQS にキューイング
    sqs.SendMessage(QueueURL, activity)
}
```

## 既存テーブルのキースキーマは変えない

### 判断

2023 年からのデータを保護するため、既存テーブル `s-nna774-net` のキースキーマは変更しない。新しい用途には別テーブルを追加する。

### 実例

初期版では全データが単一テーブルに入っていた。その後、URI で参照する用途（フォロワー・キャッシュなど）が増えたため、`s-nna774-net-kv` を別途作成。

### メリット

- 既存データの破損を防ぐ
- スキーママイグレーションが不要

### トレードオフ

- テーブルが複数になり、管理が複雑に
- 一貫性を保つ責務がアプリケーション側に

## タイムラインに JS を基本的に書かない

### 判断

HTML タイムラインは素の `<form>` と 303 リダイレクトで完結させる。JS が無くても全機能が使える状態を保つ。

### 例外

小さな UX 向上のために JavaScript を許す：
- Ctrl-Enter / Cmd-Enter で投稿送信（投稿ボタンの代替）
- いいね・ブースト（取り消しも含む）は `<form>` フォールバックを持たず、
  `fetch` 前提で JS 依存にしている。フィード上で頻繁に押すアクションが
  そのたびにページ遷移・全体再読み込みするのは UX 上のコストが大きく、
  対象はボタン自体のみでフィード全体の閲覧には影響しないため。
  成功時はページ遷移・再読み込みをせず、押されたリンク自体を DOM 上で
  逆状態のリンクに差し替える（`followBack` の `renderFollowStatus` と同じ
  やり方）。JS が無効な環境ではこの2機能だけ使えない。

### なぜ重要か

- ユーザーが JS 無効にしていても使用可能
- プライバシーが高い（トラッキングスクリプトが入りにくい）
- サーバー負荷が低い

### 実装例

```html
<!-- フォーム送信 -->
<form method="POST" action="/u/nana/statuses">
    <textarea name="content"></textarea>
    <button type="submit">投稿</button>
</form>

<!-- JavaScript：Cmd-Enter で送信（オプション） -->
<script>
document.querySelector('textarea').addEventListener('keydown', (e) => {
    if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') {
        e.target.form.submit();
    }
});
</script>
```

## Accept は goroutine で送らない

### 判断

Follow などの Activity に対する Accept レスポンスを同期で送る。goroutine で非同期実行しない。

### 理由

**Lambda のライフサイクル**:

```
1. ハンドラーが終了
2. Lambda 実行環境を凍結
3. goroutine は実行されたまま...
   → ほぼ送信されない（タイミング依存）
```

### 正しい実装

```go
// Accept を同期で送信
func handleFollow(activity *Object) {
    // ... フォロワー登録 ...
    
    // Accept を即座に送信（同期）
    sendAccept(activity)
    
    // ハンドラ終了時点で送信完了が保証される
    return nil
}

// 誤った実装
func handleFollow(activity *Object) {
    go sendAccept(activity)  // ❌ goroutine で実行
    return nil               // 実行環境が凍結される
}
```

## 画像投稿は自前ストレージではなく Gyazo に上げる

### 判断

投稿に添付する画像は S3 などの自前ストレージを持たず、Gyazo にアップロード
してその URL を `attachment` に載せる。

### 理由

- 1人用インスタンスの画像枚数ならストレージ費用はどちらでもほぼ誤差
- 自前ストレージにすると、バケットの public read 設定・Lambda での
  multipart 受信・API Gateway のペイロード上限 (10MB) 対応など、実装と
  運用の手間が増える
- Gyazo なら access token を渡して1回 API を叩くだけで済む

### トレードオフ

**メリット**:
- 実装が小さい（`gyazo.go` の1ファイル程度）
- インフラ（バケット・ポリシー）を増やさずに済む

**デメリット**:
- Gyazo 側の障害・仕様変更・レート制限に投稿画像が左右される
- 画像が自分のドメインでは配信されない

### 拡張性

`attachment` は URL を持つだけの構造にしてあるので、後で自前ストレージへ
切り替えたくなっても、アップロード先を差し替えるだけで済む。

## まとめ

これらの設計判断は、セキュリティ・可靠性・シンプルさのバランスを重視している。新しい判断を追加する際は、これらの基準に照らし合わせて検討する。
