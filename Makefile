all: dev

GO ?= /usr/local/go/bin/go
SAM := sam
REGION := ap-northeast-1
BUCKET := nana-lambda

STACK_NAME := s-nna774-net
TABLE_NAME := s-nna774-net
KV_TABLE_NAME := s-nna774-net-kv

# ローカル開発用の資格情報。本番では SSM から読む。
DEV_API_TOKEN := dev-token
DEV_SESSION_SECRET := dev-session-secret
# Gyazo は外部サービスなのでダミー値では動かない。画像投稿を試すときだけ
# `make dev DEV_GYAZO_ACCESS_TOKEN=xxx` のように渡す。
DEV_GYAZO_ACCESS_TOKEN ?=

OUT := s.nna774.net
# provided.al2023 は zip 直下の bootstrap という実行ファイルを起動する。
# template.yml の Handler の値は事実上使われない。
BOOTSTRAP := bootstrap
# デプロイパッケージに入れるものだけを置くディレクトリ。template.yml の
# CodeUri がここを指す。CodeUri を省略すると SAM はリポジトリ全体を
# zip するため、秘密鍵やソースや .git まで Lambda に同梱されてしまう。
BUILD_DIR := build

DYNAMODB_LOCAL_ENDPOINT := http://localhost:8000

app:
	$(GO) build -o $(OUT) .

dev: app
	ENV=development \
	DYNAMODB_TABLE_NAME=$(TABLE_NAME) \
	DYNAMODB_KV_TABLE_NAME=$(KV_TABLE_NAME) \
	DYNAMODB_ENDPOINT=$(DYNAMODB_LOCAL_ENDPOINT) \
	API_TOKEN=$(DEV_API_TOKEN) SESSION_SECRET=$(DEV_SESSION_SECRET) \
	GYAZO_ACCESS_TOKEN=$(DEV_GYAZO_ACCESS_TOKEN) \
	AWS_ACCESS_KEY_ID=dummy AWS_SECRET_ACCESS_KEY=dummy AWS_REGION=$(REGION) \
	./$(OUT)

test:
	$(GO) test ./...
.PHONY: test

lint:
	gofmt -l .
	$(GO) vet ./...
.PHONY: lint

# lambda.norpc は go1.x 時代の net/rpc 経由の呼び出しを落とす。
# provided.al2023 では aws-lambda-go が Runtime API を直接叩くため不要。
# CGO_ENABLED=0 で libc に依存しない静的バイナリにする。
#
# config.yml は実行時に読むので同梱が必要。逆に、それ以外は一切
# 入れてはならない。
app-for-deploy: clean
	mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 $(GO) build -tags lambda.norpc -o $(BUILD_DIR)/$(BOOTSTRAP) .
	cp config.yml $(BUILD_DIR)/config.yml
.PHONY: app-for-deploy

clean:
	rm -f $(OUT) $(BOOTSTRAP)
	rm -rf $(BUILD_DIR)
.PHONY: clean

PRIVATE_KEY := private.key
SSM_PRIVATE_KEY_PARAM := /s.nna774.net/private-key

# 公開鍵はアプリが秘密鍵から導出するので、ファイルとしては持たない。
keys:
	test -e $(PRIVATE_KEY) || openssl genrsa -out $(PRIVATE_KEY) 2048
.PHONY: keys

# 目視確認用。actor に載る publicKeyPem と同じものが出る。
pubkey:
	openssl rsa -pubout < $(PRIVATE_KEY)
.PHONY: pubkey

# SecureString の標準階層 + AWS 管理キー (aws/ssm) を使う。カスタマー
# 管理キーを作ると 1 本 1 ドル/月かかるので --key-id は指定しない。
put-key:
	aws ssm put-parameter --region $(REGION) \
		--name $(SSM_PRIVATE_KEY_PARAM) \
		--type SecureString --tier Standard --overwrite \
		--value file://$(PRIVATE_KEY)
.PHONY: put-key

deploy: app-for-deploy
	$(SAM) deploy --region $(REGION) --s3-bucket $(BUCKET) --template-file template.yml --stack-name $(STACK_NAME) --capabilities CAPABILITY_IAM
.PHONY: deploy

# --- ローカル検証 (dynamodb-local) ---

dynamodb-local:
	docker run --rm -d --name dynamodb-local -p 8000:8000 amazon/dynamodb-local
.PHONY: dynamodb-local

dynamodb-local-stop:
	docker stop dynamodb-local
.PHONY: dynamodb-local-stop

# 本番と同じキースキーマでローカルにテーブルを作る。
local-table:
	AWS_ACCESS_KEY_ID=dummy AWS_SECRET_ACCESS_KEY=dummy \
	aws dynamodb create-table \
		--endpoint-url $(DYNAMODB_LOCAL_ENDPOINT) --region $(REGION) \
		--table-name $(TABLE_NAME) \
		--attribute-definitions AttributeName=id,AttributeType=S AttributeName=num,AttributeType=N \
		--key-schema AttributeName=id,KeyType=HASH AttributeName=num,KeyType=RANGE \
		--billing-mode PAY_PER_REQUEST
	AWS_ACCESS_KEY_ID=dummy AWS_SECRET_ACCESS_KEY=dummy \
	aws dynamodb create-table \
		--endpoint-url $(DYNAMODB_LOCAL_ENDPOINT) --region $(REGION) \
		--table-name $(KV_TABLE_NAME) \
		--attribute-definitions AttributeName=pk,AttributeType=S AttributeName=sk,AttributeType=S \
		--key-schema AttributeName=pk,KeyType=HASH AttributeName=sk,KeyType=RANGE \
		--billing-mode PAY_PER_REQUEST
.PHONY: local-table
