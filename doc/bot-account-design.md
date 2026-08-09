# bot account 対応 設計

**ステータス: 未実装（設計のみ）。実装前の合意事項をまとめたもの。**

## 背景・目的

curl などから直接投稿できる bot 用アカウントが欲しい。ただし今のサーバは
[design-decisions.md](design-decisions.md) が明言するとおり「1人用 ActivityPub
サーバ」として作られており、`Config` はグローバルな単一構造体、認証トークンも
SSM 上の1本、DynamoDB のキーも全て無プレフィックスの単一アクター前提になっている。

この文書は、既存の nana アカウント（以下 primary actor）に加えて bot のような
sub actor を任意人数（N人）追加できるようにするための設計をまとめる。

## 全体方針

- **primary actor（nana）と sub actor（bot など）を区別する。両者とも同じ
  `actors:` リストの1エントリとして表現し、コード上の特別扱いは最小限にする。**
- sub actor は「投稿の作成・削除ができ、Follow だけ受け付けて独自のフォロワーを
  持てる」という縮小版の機能セットに限定する。timeline 閲覧・following 管理・
  Like/Announce/返信の受信処理は sub actor には実装しない（v1 スコープ外）。
- 何人 sub actor を増やしても同じ仕組みで足りるように、config・ルーティング・
  認証・datastore の各層を「アクターの配列 / マップ」を前提にした作りにする。

## config

`config.yml` を単一の平坦な `Config` ではなく、アクターのリストにする。

```yaml
actors:
  - username: 'nana@s.nna774.net'
    primary: true
    name: 久我山菜々
    actor_type: Person
    icon_uri: https://nna774.net/img/1012_filtered.jpg
    summary: B95 H108 S102
    auto_accept_follow: true
    hide_collections: false
    private_key_parameter: /s.nna774.net/private-key
    api_token_parameter: /s.nna774.net/api-token
    # 既存の alias_usernames / fields / session_secret_parameter /
    # gyazo_access_token_parameter 等はそのまま primary actor 側に残す

  - username: 'bot@s.nna774.net'
    name: bot
    actor_type: Service   # default. Mastodon 上で bot ラベルが付く
    auto_accept_follow: true   # default true
    private_key_parameter: /s.nna774.net/bot-private-key
    api_token_parameter: /s.nna774.net/bot-api-token
```

- `primary: true` を持つエントリが1つだけ存在する前提（nana）。webfinger の
  デフォルト解決・トップページの `/u/nana` リダイレクトなど、instance の
  「代表」が要る箇所はこれを見る。
- `actor_type` は per-actor 設定可能、default は `Service`。primary actor は
  現状維持のため明示的に `Person` を指定する。
- `auto_accept_follow` は per-actor 設定可能、default true。
- origin・session_secret 等インスタンス全体で1つでよいものは `actors:` の外、
  トップレベルに残す。

## ルーティング

`main.go` の各ハンドラは現状 `:user` パスパラメータを実質無視して常に
グローバル `Config` を見ている。これを `:user` の localpart で `actors:` を
引く方式に変える。

```go
func resolveActor(c *gin.Context) (*config.ActorConfig, error) {
    localpart := c.Param("user")
    actor, ok := actorsByLocalPart[localpart]
    if !ok {
        return nil, ErrActorNotFound
    }
    return actor, nil
}
```

if/else の特別扱いではなく、localpart をキーにしたマップ引きにする。何人
増えてもこの1関数だけで対応できる。

## 認証（トークンのアクター紐付け）

各アクターの API トークンは、そのアクター自身の private エンドポイントにしか
使えない。トークンを検証する際、トークンの一致だけでなく「そのトークンが
パス上の `:user` に対応するアクターのものか」も確認する。bot のトークンで
nana のエンドポイントを叩けたり、その逆が起きたりしないようにする。

## sub actor の private エンドポイント範囲

sub actor（primary 以外）が持つ private エンドポイントは以下のみ:

- `POST /u/:user/statuses`（投稿作成）
- `DELETE /u/:user/status/:id`（投稿削除）

timeline 閲覧・notifications 閲覧・following 管理（フォロー追加/削除）は
sub actor には実装しない。これらは primary actor 専用のまま。

## inbox のスコープ

sub actor の inbox は `Follow` / `Undo Follow` のみ処理し、フォロワーを
蓄積する（`auto_accept_follow` に従い自動承認）。それ以外の Activity 種別
（Like・Announce・Create（返信）・Undo（Follow 以外）等）は HTTP Signature の
検証・`keyId`/`actor` の一致確認は今まで通り実施した上で、中身は処理せず
黙って受理（202 等）して捨てる。なりすまし対策としての検証は省略しない。

## 通知

sub actor 宛の Follow / フォロワー増減も、primary actor（nana）の
`/notifications` に統合して表示する。通知フィード自体はインスタンス全体で
共有の単一ストリーム（後述の datastore 設計で「共有リソース」として扱う）。

## datastore のキー設計

**方針**: アクター固有のリソースは全アクター（nana を含む）を対称に扱い、
`<localpart>:<resource>` で名前空間を切る。インスタンス全体で共有すべき
リソース（アクターに依存しない情報）は無プレフィックスのまま共有する。
「nana だけ無プレフィックスの特例」というレガシー互換の非対称性は作らない
（一度きりの移行を許容し、対称なモデルにする）。

`datastore` パッケージ自体は opaque な文字列キーしか扱っていないため、
`datastore.go` は変更不要。呼び出し側（`main.go` / `status.go` /
`notification.go` 等）が渡す `name` / `key` にアクターの localpart を
前置するだけで実現できる。

```go
func actorScoped(actor *config.ActorConfig, name string) string {
    return actor.LocalPart() + ":" + name
}
```

アクター固有のアクセスはこのヘルパーを通し、共有リソースは今まで通り
素の定数を渡す。

### メインテーブル（`s-nna774-net`）

| 論理名 | 現在の PK | 移行後（nana） | bot |
|---|---|---|---|
| outbox | `object--outbox` / `counter--outbox` | `object--nana:outbox` / `counter--nana:outbox` | `object--bot:outbox` / `counter--bot:outbox` |
| status | `object--status` / `counter--status` | `object--nana:status` / `counter--nana:status` | `object--bot:status` / `counter--bot:status` |
| notification（共有） | `object--notification` / `counter--notification` | 変更なし | 同上（共有） |

### KV テーブル（`s-nna774-net-kv`）

| 種別 | アクター固有（プレフィックス付き） | 共有のまま（無プレフィックス） |
|---|---|---|
| PK | `nana:followers` `bot:followers` `nana:following` `bot:following` `nana:mylikes` `nana:myboosts` `nana:likes` `nana:announced` `nana:myboostbyid` 等 | `actorkey`（リモート actor 公開鍵キャッシュ）`seen`（Activity ID 重複排除）`actorinfo`（リモート actor 表示情報キャッシュ）`cursor`（通知フィードの既読位置。読者は primary actor だけなので共有のままでよい） |

共有側は「どのローカルアクター宛に届いたか」に依存しない情報（リモート側の
鍵・重複排除・表示キャッシュ・単一読者の既読位置）なので、sub actor が
何人増えてもそのまま使い回せる。

### nana の移行対象

- メインテーブル: `object--outbox`→`object--nana:outbox`、
  `counter--outbox`→`counter--nana:outbox`、
  `object--status`→`object--nana:status`、
  `counter--status`→`counter--nana:status`
- KV テーブル PK: `followers`→`nana:followers`、`following`→`nana:following`、
  `mylikes`→`nana:mylikes`、`myboosts`→`nana:myboosts`、`likes`→`nana:likes`、
  `announced`→`nana:announced`、`myboostbyid`→`nana:myboostbyid`
- `actorkey` / `seen` / `actorinfo` / `cursor` / `notification` 系は移行不要

このプロジェクトはトラフィックがほぼゼロの1人用インスタンスのため、移行は
メンテナンスウィンドウなしで実施してよいと合意済み。

design-decisions.md の「既存テーブルのキースキーマは変えない」という方針は、
この bot account 対応に関しては例外として扱ってよいと合意済み（本ドキュメント
がその例外の記録を兼ねる）。

## 鍵・トークン運用

`make keys` / `make put-key` / `make pubkey` は現在1アクター専用の実装なので、
`ACTOR=xxx` のような引数を取れるように拡張する（例: `make keys ACTOR=bot`）。
sub actor もそれぞれ独立した RSA 鍵ペアと API トークンを持つ。

## v1 のスコープ外（将来必要になったら検討）

- sub actor の Like / Announce / 返信の受信処理
- sub actor 専用の timeline 閲覧・following 管理 UI
- sub actor の投稿に対する自分からの Like / Announce（フォローしないので不要）
- primary actor 以外を複数持つ場合の webfinger 上のデフォルト解決の曖昧さ
  （`primary: true` は1つだけという前提が崩れるケース）
