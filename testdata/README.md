# testdata

ActivityPub の相互運用性テスト用ペイロード。いずれも 2023 年の開発時に
作業ツリーへ置かれていたもので、git には入らないまま 2023-09-09 の
デプロイパッケージ内にのみ残っていた。そこから回収した。

| ファイル | 由来 | 用途 |
|---|---|---|
| `follow_outgoing.json` | 手書き。自分 → `pawoo.net/users/kugayama` への Follow | 送信 Follow の形の確認 |
| `follow_incoming.json` | 手書き。`pawoo.net/users/kugayama` → 自分への Follow | **`object` が文字列URIである**受信 Follow。decode できることの回帰テスト |
| `create_note.json` | `actub.ub32.org` の実ペイロード | `Create` に `Note` が入れ子になった形 |
| `outbox_page_mastodon.json` | `pawoo.net` の実 outbox ページ | Mastodon の `OrderedCollectionPage`。`@context` が配列とオブジェクトの混在、`ostatus:`/`toot:` 拡張を含む |

`follow_incoming.json` が最も重要だ。ActivityPub では `object` /
`actor` / `attributedTo` / `inReplyTo` が「文字列URI」と「埋め込み
オブジェクト」のどちらでも来る。文字列側を decode できなかったために
inbox が 422 を返し、誰もこのインスタンスをフォローできない状態が
長く続いていた。このファイルはその回帰テストとして使う。

内容はキャプチャしたそのままの状態で置いてある(`outbox_page_mastodon.json`
が1行なのも実データのため)。整形しないこと。
