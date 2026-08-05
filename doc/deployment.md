# デプロイ

## デプロイ手順

### 自動デプロイ

```sh
make deploy
```

このコマンドで以下が自動実行される：

1. `provided.al2023` ランタイムでスタティックビルド
2. arm64 アーキテクチャ用にコンパイル
3. `build/bootstrap` に配置
4. `build/config.yml` をコピー
5. AWS SAM でデプロイ

**重要**: `build/` には `bootstrap` と `config.yml` だけが含まれる。リポジトリ全体は Lambda に含まれない。

### デプロイ前の確認

```sh
# ビルドだけしたい場合
make build

# SAM テンプレートの確認
cat template.yaml

# 秘密情報が SSM に設定されているか確認
aws ssm describe-parameters --region ap-northeast-1 | grep s.nna774.net
```

## 秘密情報の初期セットアップ

### 1. 署名鍵の生成と登録

```sh
# 秘密鍵を生成（開発時）
make keys

# SSM に登録
make put-key

# または手動で:
aws ssm put-parameter --region ap-northeast-1 \
  --name /s.nna774.net/private-key \
  --type SecureString --tier Standard \
  --value "$(cat private.key)" \
  --overwrite
```

### 2. API トークンの生成と登録

```sh
# ランダムな Base64 文字列を生成
TOKEN=$(openssl rand -base64 32)
echo $TOKEN

# SSM に登録
aws ssm put-parameter --region ap-northeast-1 \
  --name /s.nna774.net/api-token \
  --type SecureString --tier Standard \
  --value "$TOKEN"
```

秘密を確認（検証目的）:

```sh
aws ssm get-parameter --region ap-northeast-1 \
  --name /s.nna774.net/api-token \
  --with-decryption \
  --query 'Parameter.Value'
```

### 3. Cookie 署名鍵の生成と登録

```sh
# ランダムな Base64 文字列を生成
SECRET=$(openssl rand -base64 32)
echo $SECRET

# SSM に登録
aws ssm put-parameter --region ap-northeast-1 \
  --name /s.nna774.net/session-secret \
  --type SecureString --tier Standard \
  --value "$SECRET"
```

### 4. Gyazo アクセストークンの登録（画像投稿を使う場合）

画像投稿は Gyazo にアップロードする方式なので、[Gyazo API](https://gyazo.com/api)
でアプリを登録してアクセストークンを発行し、SSM に登録する。

```sh
aws ssm put-parameter --region ap-northeast-1 \
  --name /s.nna774.net/gyazo-access-token \
  --type SecureString --tier Standard \
  --value "$GYAZO_ACCESS_TOKEN"
```

`config.yml` の `gyazo_access_token_parameter` が空、またはこのパラメータが
未登録のままだと、画像添付付きの投稿はエラーになる（文章だけの投稿は影響
しない）。

## 秘密情報の管理

### SSM Parameter Store について

- **標準階層** (`--tier Standard`)：無料
  - 保存料・API 料・KMS 月額いずれも発生しない
- **高度階層** (`--tier Advanced`)：$1.00/月
  - 不要（使わない）
- **カスタマー管理キー** (`--kms-key-id`)：$1.00/月
  - 不要。AWS 管理キー (`aws/ssm`) で十分

### 秘密鍵の形式

- PKCS#1 形式：`-----BEGIN RSA PRIVATE KEY-----`
- PKCS#8 形式：`-----BEGIN PRIVATE KEY-----`

どちらでも読める。OpenSSL 3 の `genrsa` は PKCS#8 を出力する。

## CloudWatch Logs 設定

Lambda の標準出力は自動で `/aws/lambda/s-nna774-net` に記録される。

### リテンション期間の設定

ログの肥大化防止のため 30 日に設定：

```sh
aws logs put-retention-policy --region ap-northeast-1 \
  --log-group-name /aws/lambda/s-nna774-net \
  --retention-in-days 30
```

### ログの確認

```sh
# 直近のログを表示
aws logs tail /aws/lambda/s-nna774-net --follow

# または CloudWatch コンソール:
# https://console.aws.amazon.com/cloudwatch/home?region=ap-northeast-1#logsV2:log-groups
```

## デプロイ時の注意

### CodeUri は必ず `build/` にする

```yaml
# 正しい例（template.yaml）
Resources:
  LambdaFunction:
    Type: AWS::Serverless::Function
    Properties:
      CodeUri: build/
      Handler: bootstrap
      Runtime: provided.al2023
      Architectures:
        - arm64
```

### なぜ重要か？

`CodeUri` を省略するとリポジトリ全体が zip され、秘密鍵・ソース・`.git` が Lambda に同梱される。

```
危険：
- lambda:GetFunction の権限がある者が秘密鍵を取得可能
- リポジトリの全履歴が公開される
```

## スケーリング・パフォーマンス

### DynamoDB オンデマンド課金

1人用で事実上ゼロトラフィックのため、オンデマンド課金が最安価。

```
コスト比較（月額）:
- オンデマンド： 数円程度
- プロビジョンド 1 RCU / 1 WCU： $0.65
```

### Lambda のリソース

デフォルト 128 MB メモリ。必要に応じて増加：

```yaml
# template.yaml
MemorySize: 256  # デフォルト 128
Timeout: 30      # デフォルト 30 秒
```

配信が多い場合はタイムアウト時間を延長。

## トラブルシューティング

### デプロイが失敗する

```
Error: Unable to upload artifact
```

確認:

```sh
# AWS 資格情報が設定されているか
aws sts get-caller-identity

# リージョンが正しいか
aws configure get region
```

### Lambda の起動に失敗

```
Task timed out after 30 seconds
```

原因:
- DynamoDB へのアクセスが遅い（ネットワーク遅延）
- タイムアウト時間が足りない
- 配信先が多すぎる

対策:
- タイムアウト時間を 60 秒に延長
- 配信を SQS 経由の非同期処理に変更（大規模時）

### SSM から秘密情報が読めない

```
InvalidParameterName
```

確認:

```sh
# パラメータが存在するか
aws ssm describe-parameters --region ap-northeast-1

# 値を取得できるか
aws ssm get-parameter --region ap-northeast-1 \
  --name /s.nna774.net/api-token \
  --with-decryption
```

### CORS エラー

API Gateway が CORS ヘッダを返していない場合:

```yaml
# template.yaml で CORS を設定
Cors:
  AllowMethods: "'*'"
  AllowHeaders: "'*'"
  AllowOrigin: "'*'"
```

## ロールバック

### 前のバージョンに戻す

```sh
# デプロイ履歴を確認
aws cloudformation describe-stacks --stack-name s-nna774-net --region ap-northeast-1

# ロールバック
aws cloudformation cancel-update-stack --stack-name s-nna774-net --region ap-northeast-1
```

または手動で AWS コンソールから。

## 本番モニタリング

### CloudWatch ダッシュボード

```sh
# カスタムメトリクスを送信（オプション）
aws cloudwatch put-metric-data --metric-name Requests \
  --value 1 --namespace s.nna774.net
```

### アラート設定（推奨）

```sh
aws cloudwatch put-metric-alarm \
  --alarm-name s-nna774-net-errors \
  --alarm-description "Lambda errors" \
  --metric-name Errors \
  --namespace AWS/Lambda \
  --statistic Sum \
  --period 300 \
  --threshold 5 \
  --comparison-operator GreaterThanThreshold
```

## 定期メンテナンス

### 月1回の定期確認

```sh
# ビルドと簡易テスト
make build
make test

# SSM パラメータの存在確認
aws ssm describe-parameters --region ap-northeast-1 | grep s.nna774.net

# CloudWatch Logs サイズ確認
aws logs describe-log-groups --log-group-name-prefix /aws/lambda/s-nna774-net --region ap-northeast-1
```

### 年1回の秘密鍵ローテーション（オプション）

```sh
# 新しい鍵を生成して登録
make keys
make put-key --overwrite

# Actor に新しい公開鍵が載っているか確認
make pubkey
```
