// apclient は署名付きの ActivityPub リクエストを手で投げるための道具。
// 連合まわりの動作確認に使う。
//
//	go run ./tools/apclient -post https://example.com/u/x/inbox -body follow.json
//	go run ./tools/apclient -get https://pawoo.net/users/kugayama
//
// 秘密鍵は config.yml の設定に従って読む。ENV=development ならファイル、
// それ以外は SSM から取る。
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"

	"github.com/nna774/s.nna774.net/config"
	"github.com/nna774/s.nna774.net/httpsigclient"
)

func main() {
	var (
		configFile = flag.String("config", "config.yml", "path to config.yml")
		region     = flag.String("region", "ap-northeast-1", "AWS region for SSM")
		postTo     = flag.String("post", "", "URL to POST a signed activity to")
		getFrom    = flag.String("get", "", "URL to GET with a signature")
		bodyFile   = flag.String("body", "", "file containing the activity to POST ('-' for stdin)")
	)
	flag.Parse()

	if (*postTo == "") == (*getFrom == "") {
		log.Fatal("give exactly one of -post or -get")
	}

	ctx := context.Background()
	cnf, err := config.LoadConfig(ctx, *configFile, *region)
	if err != nil {
		log.Fatalf("loading config: %v", err)
	}
	keyID := cnf.ID() + "#main-key"
	signer, err := httpsigclient.NewSigner(cnf.PrivateKey(), cnf.PublicKey(), keyID)
	if err != nil {
		log.Fatalf("creating signer: %v", err)
	}
	fmt.Fprintf(os.Stderr, "signing as %s\n", keyID)

	var resp *http.Response
	if *getFrom != "" {
		resp, err = signer.GetWithSign(ctx, *getFrom)
	} else {
		body, rerr := readBody(*bodyFile)
		if rerr != nil {
			log.Fatalf("reading body: %v", rerr)
		}
		resp, err = signer.RequestWithSign(ctx, http.MethodPost, *postTo, body)
	}
	if err != nil {
		log.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	fmt.Fprintf(os.Stderr, "%s\n", resp.Status)
	if _, err := io.Copy(os.Stdout, resp.Body); err != nil {
		log.Fatalf("reading response: %v", err)
	}
	if resp.StatusCode/100 != 2 {
		os.Exit(1)
	}
}

func readBody(path string) ([]byte, error) {
	if path == "" {
		return nil, fmt.Errorf("-body is required with -post")
	}
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}
