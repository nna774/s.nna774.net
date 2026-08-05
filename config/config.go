package config

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"mime"
	"os"
	"path"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"gopkg.in/yaml.v2"
)

type Config struct {
	Username       string   `yaml:"username"`
	AliasUsernames []string `yaml:"alias_usernames"`
	Name           string   `yaml:"name"`
	Origin         string   `yaml:"origin"`
	IconURI        string   `yaml:"icon_uri"`
	Summary        string   `yaml:"summary"`
	PublicKeyName  string   `yaml:"public_key_name"`

	// Fields はプロフィールに並べる追加情報。Mastodon 系は actor の
	// attachment に入っている PropertyValue をプロフィール欄の表として
	// 表示する。
	Fields []Field `yaml:"fields"`

	// AutoAcceptFollow が false の場合、受け取った Follow は pending で
	// 保留し、手動で Accept するまでフォロワーに数えない。
	AutoAcceptFollow bool `yaml:"auto_accept_follow"`
	// HideCollections が true の場合、followers / following の中身と件数を
	// 公開しない。
	HideCollections bool `yaml:"hide_collections"`

	// PrivateKeyFile は ENV=development のときだけ使う。
	PrivateKeyFile string `yaml:"private_key_file"`
	// PrivateKeyParameter は SSM Parameter Store のパラメータ名。
	// ENV=development 以外ではこちらから読む。
	PrivateKeyParameter string `yaml:"private_key_parameter"`
	// APITokenParameter は私用エンドポイントの資格情報。
	APITokenParameter string `yaml:"api_token_parameter"`
	// SessionSecretParameter は Cookie 署名用の HMAC 鍵。API トークンとは
	// 別に持つことで、鍵を差し替えるだけで全セッションを失効させられる。
	SessionSecretParameter string `yaml:"session_secret_parameter"`
	// GyazoAccessTokenParameter は画像投稿で使う Gyazo API のアクセス
	// トークン。空なら画像投稿機能は無効。
	GyazoAccessTokenParameter string `yaml:"gyazo_access_token_parameter"`

	privateKey       *rsa.PrivateKey
	publicKey        string
	apiToken         string
	sessionSecret    string
	gyazoAccessToken string
}

// Field はプロフィールの1項目。Value が http(s) の URL のときはリンクに
// なる。リンク先に actor へ戻る rel="me" のリンクがあれば、Mastodon は
// その項目を検証済みとして表示する。
type Field struct {
	Name  string `yaml:"name"`
	Value string `yaml:"value"`
}

// IsDevelopment はローカル実行かどうかを返す。秘密情報をファイルから
// 読むか SSM から読むかの分岐に使う。
func IsDevelopment() bool { return os.Getenv("ENV") == "development" }

func (c *Config) LocalPart() string {
	return strings.SplitN(c.Username, "@", 2)[0]
}

func (c *Config) ID() string {
	return c.Origin + "/u/" + c.LocalPart() // TODO: ? kimeuchi
}

func (c *Config) IconMediaType() string {
	if t := mime.TypeByExtension(path.Ext(c.IconURI)); t != "" {
		return t
	}
	return "application/octet-stream"
}

func (c *Config) PrivateKey() *rsa.PrivateKey { return c.privateKey }
func (c *Config) PublicKey() string           { return c.publicKey }
func (c *Config) APIToken() string            { return c.apiToken }
func (c *Config) SessionSecret() string       { return c.sessionSecret }
func (c *Config) GyazoAccessToken() string    { return c.gyazoAccessToken }

// 開発時の秘密情報は環境変数で渡す。SSM を引かずに動かせるようにする。
const (
	devAPITokenEnv         = "API_TOKEN"
	devSessionSecretEnv    = "SESSION_SECRET"
	devGyazoAccessTokenEnv = "GYAZO_ACCESS_TOKEN"
)

func (c *Config) loadSecrets(ctx context.Context, region string) error {
	if IsDevelopment() {
		if c.PrivateKeyFile == "" {
			return errors.New("private_key_file is required when ENV=development")
		}
		buf, err := os.ReadFile(c.PrivateKeyFile)
		if err != nil {
			return err
		}
		if err := c.setPrivateKey(buf); err != nil {
			return err
		}
		c.apiToken = os.Getenv(devAPITokenEnv)
		c.sessionSecret = os.Getenv(devSessionSecretEnv)
		c.gyazoAccessToken = os.Getenv(devGyazoAccessTokenEnv)
		return nil
	}

	if c.PrivateKeyParameter == "" {
		return errors.New("private_key_parameter is required")
	}
	// まとめて1回の API 呼び出しで引く。コールドスタートで叩く KMS の
	// 回数を抑えられる。
	names := []string{c.PrivateKeyParameter}
	if c.APITokenParameter != "" {
		names = append(names, c.APITokenParameter)
	}
	if c.SessionSecretParameter != "" {
		names = append(names, c.SessionSecretParameter)
	}
	if c.GyazoAccessTokenParameter != "" {
		names = append(names, c.GyazoAccessTokenParameter)
	}
	params, err := fetchParameters(ctx, region, names...)
	if err != nil {
		return err
	}
	if err := c.setPrivateKey([]byte(params[c.PrivateKeyParameter])); err != nil {
		return err
	}
	c.apiToken = params[c.APITokenParameter]
	c.sessionSecret = params[c.SessionSecretParameter]
	c.gyazoAccessToken = params[c.GyazoAccessTokenParameter]
	return nil
}

func (c *Config) setPrivateKey(buf []byte) error {
	block, _ := pem.Decode(buf)
	if block == nil {
		return errors.New("invalid private key data")
	}
	key, err := parseRSAPrivateKey(block.Bytes)
	if err != nil {
		return err
	}
	c.privateKey = key

	// 公開鍵は秘密鍵から導出する。別ファイルや別パラメータで持つと、
	// 食い違ったままの公開鍵を actor に載せてしまう余地が残り、その場合
	// リモート側での署名検証が通らなくなる。導出しておけば構造的に
	// 起こり得ない。
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return err
	}
	c.publicKey = string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	return nil
}

// parseRSAPrivateKey は PKCS#1 と PKCS#8 の両方を受ける。既存の鍵は
// PKCS#1 (BEGIN RSA PRIVATE KEY) だが、OpenSSL 3 の genrsa は PKCS#8
// (BEGIN PRIVATE KEY) を出すため、鍵を作り直したときに読めなくなる。
func parseRSAPrivateKey(der []byte) (*rsa.PrivateKey, error) {
	if key, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, fmt.Errorf("private key is neither PKCS#1 nor PKCS#8: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key must be RSA, got %T", parsed)
	}
	return key, nil
}

// fetchParameters は SSM Parameter Store から SecureString を取得する。
// 1回の API 呼び出しでまとめて引くため、コールドスタートで叩く KMS の
// 回数を抑えられる。
func fetchParameters(ctx context.Context, region string, names ...string) (map[string]string, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, err
	}
	out, err := ssm.NewFromConfig(cfg).GetParameters(ctx, &ssm.GetParametersInput{
		Names:          names,
		WithDecryption: aws.Bool(true),
	})
	if err != nil {
		return nil, fmt.Errorf("ssm GetParameters failed: %w", err)
	}
	if len(out.InvalidParameters) > 0 {
		return nil, fmt.Errorf("ssm parameters not found: %v", out.InvalidParameters)
	}
	res := make(map[string]string, len(out.Parameters))
	for _, p := range out.Parameters {
		res[aws.ToString(p.Name)] = aws.ToString(p.Value)
	}
	return res, nil
}

func LoadConfig(ctx context.Context, configFile string, region string) (*Config, error) {
	cfg, err := os.ReadFile(configFile)
	if err != nil {
		return nil, err
	}
	var config = &Config{}
	err = yaml.UnmarshalStrict(cfg, config)
	if err != nil {
		return nil, err
	}
	err = config.loadSecrets(ctx, region)
	return config, err
}
