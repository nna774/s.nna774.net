# ドキュメント目次

`s.nna774.net` は1人用の ActivityPub サーバ。Go + AWS Lambda + DynamoDB で動作する。

## ドキュメント構成

- [README](../README.md) - プロジェクト概要
- [Architecture](architecture.md) - システムアーキテクチャ・構成図
- [Packages](packages.md) - パッケージ構成と責務
- [Endpoints](endpoints.md) - エンドポイント一覧
- [Development](development.md) - ローカル開発の手順
- [Deployment](deployment.md) - デプロイ手順
- [Design Decisions](design-decisions.md) - 設計上の判断と理由
- [Bot Account Design](bot-account-design.md) - 複数アクター（bot account）対応の設計（未実装）
- [Known Issues](known-issues.md) - 対応方針を保留している既知の課題

## クイックスタート

### ローカル開発

```sh
make dynamodb-local   # docker で dynamodb-local を起動
make local-table      # テーブルを2本作成
make dev              # localhost:8080 で起動
```

### テスト・ビルド

```sh
make test
make lint
make deploy           # AWS にデプロイ
```

## 認証

秘密は SSM に置いた共有シークレット1本 (`api-token`) で、受け口が2つある。

- **curl**: `Authorization: Bearer <token>`
- **ブラウザ**: `POST /login` にトークンを渡すと HMAC-SHA256 署名の Cookie が発行される

## 費用

DynamoDB はオンデマンド課金。1人用でトラフィックが実質ゼロなので非常に安価。CloudWatch Logs は 30 日で自動削除される設定。
