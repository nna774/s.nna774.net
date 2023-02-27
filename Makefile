all: dev

SAM := sam
REGION := ap-northeast-1
BUCKET := nana-lambda

STACK_NAME := s-nna774-net

OUT := s.nna774.net

app:
	go build -o $(OUT) .

dev: app
	ENV=development DYNAMODB_TABLE_NAME=s-nna774-net ./$(OUT)

app-for-deploy: clean
	GOARCH=amd64 GOOS=linux go build -o $(OUT) .
.PHONY: app-for-deploy

clean:
	rm -f $(OUT)
.PHONY: clean

PRIVATE_KEY := private.key
PUBLIC_KEY := pub.key

keys:
	test -e	$(PRIVATE_KEY) || openssl genrsa 2048 -out $(PRIVATE_KEY)
	openssl rsa -pubout < $(PRIVATE_KEY) > $(PUBLIC_KEY)

deploy: app-for-deploy
	$(SAM) deploy --region $(REGION) --s3-bucket $(BUCKET) --template-file template.yml --stack-name $(STACK_NAME) --capabilities CAPABILITY_IAM
