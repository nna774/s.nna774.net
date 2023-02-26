all: app

SAM := sam
REGION := ap-northeast-1
BUCKET := nana-lambda

STACK_NAME := s-nna774-net

app:
	go build

app-for-deploy:
	GOARCH=amd64 GOOS=linux go build

deploy: app-for-deploy
	$(SAM) deploy --region $(REGION) --s3-bucket $(BUCKET) --template-file template.yml --stack-name $(STACK_NAME) --capabilities CAPABILITY_IAM
