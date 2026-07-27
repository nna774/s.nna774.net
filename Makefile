all: dev

SAM := sam
REGION := ap-northeast-1
BUCKET := nana-lambda

STACK_NAME := s-nna774-net
TABLE_NAME := s-nna774-net

OUT := s.nna774.net
# provided.al2023 は zip 直下の bootstrap という実行ファイルを起動する。
# template.yml の Handler の値は事実上使われない。
BOOTSTRAP := bootstrap

DYNAMODB_LOCAL_ENDPOINT := http://localhost:8000

app:
	go build -o $(OUT) .

dev: app
	ENV=development \
	DYNAMODB_TABLE_NAME=$(TABLE_NAME) \
	DYNAMODB_ENDPOINT=$(DYNAMODB_LOCAL_ENDPOINT) \
	AWS_ACCESS_KEY_ID=dummy AWS_SECRET_ACCESS_KEY=dummy AWS_REGION=$(REGION) \
	./$(OUT)

test:
	go test ./...
.PHONY: test

lint:
	gofmt -l .
	go vet ./...
.PHONY: lint

# lambda.norpc は go1.x 時代の net/rpc 経由の呼び出しを落とす。
# provided.al2023 では aws-lambda-go が Runtime API を直接叩くため不要。
# CGO_ENABLED=0 で libc に依存しない静的バイナリにする。
app-for-deploy: clean
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -tags lambda.norpc -o $(BOOTSTRAP) .
.PHONY: app-for-deploy

clean:
	rm -f $(OUT) $(BOOTSTRAP)
.PHONY: clean

PRIVATE_KEY := private.key
PUBLIC_KEY := pub.key

keys:
	test -e $(PRIVATE_KEY) || openssl genrsa -out $(PRIVATE_KEY) 2048
	openssl rsa -pubout < $(PRIVATE_KEY) > $(PUBLIC_KEY)
.PHONY: keys

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
.PHONY: local-table
