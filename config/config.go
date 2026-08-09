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

// ActorType の取りうる値。Service は bot 等の自動投稿アカウントであることを
// Mastodon 等に伝える。
const (
	ActorTypePerson  = "Person"
	ActorTypeService = "Service"
)

// Config はインスタンス全体の設定。1インスタンスに複数の Actor
// (primary actor 1人 + sub actor 何人か) を持てる。
type Config struct {
	Origin        string `yaml:"origin"`
	PublicKeyName string `yaml:"public_key_name"`
	// SessionSecretParameter は Cookie 署名用の HMAC 鍵。ブラウザでのログインは
	// primary actor だけが行うため、Actor ごとではなくインスタンスに1つ。
	SessionSecretParameter string `yaml:"session_secret_parameter"`
	// GyazoAccessTokenParameter は画像投稿で使う Gyazo API のアクセス
	// トークン。空なら画像投稿機能は無効。全 Actor で共有する。
	GyazoAccessTokenParameter string `yaml:"gyazo_access_token_parameter"`

	Actors []*ActorConfig `yaml:"actors"`

	sessionSecret    string
	gyazoAccessToken string
}

// ActorConfig は1つの Actor (primary actor である nana、あるいは bot 等の
// sub actor) の設定。
type ActorConfig struct {
	Username       string   `yaml:"username"`
	AliasUsernames []string `yaml:"alias_usernames"`
	// Primary が true の Actor がちょうど1人いる前提。webfinger のデフォルト
	// 解決やトップページのリダイレクト先、timeline/notifications など
	// primary actor 専用のエンドポイントはこれを見る。
	Primary bool   `yaml:"primary"`
	Name    string `yaml:"name"`
	// ActorType は "Person" か "Service"。省略不可 (Person / Service の
	// どちらを意図しているか設定ファイルから常に読み取れるようにするため)。
	ActorType string `yaml:"actor_type"`
	IconURI   string `yaml:"icon_uri"`
	Summary   string `yaml:"summary"`

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
	// APITokenParameter は私用エンドポイントの資格情報。Actor ごとに別の
	// トークンを持つ。あるトークンで別の Actor のエンドポイントは叩けない。
	APITokenParameter string `yaml:"api_token_parameter"`

	// Origin は親 Config からコピーする (YAML には出さない)。ID() を
	// Actor 単体で完結させるため。
	Origin string `yaml:"-"`

	privateKey *rsa.PrivateKey
	publicKey  string
	apiToken   string
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

func (a *ActorConfig) LocalPart() string {
	return strings.SplitN(a.Username, "@", 2)[0]
}

func (a *ActorConfig) ID() string {
	return a.Origin + "/u/" + a.LocalPart() // TODO: ? kimeuchi
}

func (a *ActorConfig) IconMediaType() string {
	if t := mime.TypeByExtension(path.Ext(a.IconURI)); t != "" {
		return t
	}
	return "application/octet-stream"
}

func (a *ActorConfig) PrivateKey() *rsa.PrivateKey { return a.privateKey }
func (a *ActorConfig) PublicKey() string           { return a.publicKey }
func (a *ActorConfig) APIToken() string            { return a.apiToken }

func (c *Config) SessionSecret() string    { return c.sessionSecret }
func (c *Config) GyazoAccessToken() string { return c.gyazoAccessToken }

// PrimaryActor は primary actor (nana) の設定を返す。LoadConfig がちょうど
// 1人であることを検証済みなので必ず見つかる。
func (c *Config) PrimaryActor() *ActorConfig {
	for _, a := range c.Actors {
		if a.Primary {
			return a
		}
	}
	return nil
}

// ActorByLocalPart は URL の :user に対応する Actor を探す。
func (c *Config) ActorByLocalPart(localPart string) (*ActorConfig, bool) {
	for _, a := range c.Actors {
		if a.LocalPart() == localPart {
			return a, true
		}
	}
	return nil, false
}

// 開発時の秘密情報は環境変数で渡す。SSM を引かずに動かせるようにする。
const (
	devAPITokenEnv         = "API_TOKEN"
	devSessionSecretEnv    = "SESSION_SECRET"
	devGyazoAccessTokenEnv = "GYAZO_ACCESS_TOKEN"
)

// devAPITokenEnvName は開発時にこの Actor のトークンをどの環境変数から
// 読むか。primary actor は既存の互換性のため API_TOKEN のまま、それ以外は
// "<LOCALPART>_API_TOKEN" (例: bot なら BOT_API_TOKEN)。
func (a *ActorConfig) devAPITokenEnvName() string {
	if a.Primary {
		return devAPITokenEnv
	}
	return strings.ToUpper(a.LocalPart()) + "_API_TOKEN"
}

func (a *ActorConfig) loadSecretsDev() error {
	if a.PrivateKeyFile == "" {
		return fmt.Errorf("actor %v: private_key_file is required when ENV=development", a.Username)
	}
	buf, err := os.ReadFile(a.PrivateKeyFile)
	if err != nil {
		return err
	}
	if err := a.setPrivateKey(buf); err != nil {
		return err
	}
	a.apiToken = strings.TrimSpace(os.Getenv(a.devAPITokenEnvName()))
	return nil
}

func (c *Config) loadSecrets(ctx context.Context, region string) error {
	if IsDevelopment() {
		c.sessionSecret = strings.TrimSpace(os.Getenv(devSessionSecretEnv))
		c.gyazoAccessToken = strings.TrimSpace(os.Getenv(devGyazoAccessTokenEnv))
		for _, a := range c.Actors {
			if err := a.loadSecretsDev(); err != nil {
				return err
			}
		}
		return nil
	}

	// まとめて1回の API 呼び出しで引く。コールドスタートで叩く KMS の
	// 回数を抑えられる。
	names := []string{}
	if c.SessionSecretParameter != "" {
		names = append(names, c.SessionSecretParameter)
	}
	if c.GyazoAccessTokenParameter != "" {
		names = append(names, c.GyazoAccessTokenParameter)
	}
	for _, a := range c.Actors {
		if a.PrivateKeyParameter == "" {
			return fmt.Errorf("actor %v: private_key_parameter is required", a.Username)
		}
		names = append(names, a.PrivateKeyParameter)
		if a.APITokenParameter != "" {
			names = append(names, a.APITokenParameter)
		}
	}
	params, err := fetchParameters(ctx, region, names...)
	if err != nil {
		return err
	}
	// SSM に登録するとき --value file://... で末尾改行付きのファイルを
	// そのまま渡してしまうと、トークンの末尾に "\n" が混入する。API に
	// 渡すと本体は合っているのに認証だけ落ちる、気づきにくい壊れ方をする
	// ため、ここで trim しておく。
	c.sessionSecret = strings.TrimSpace(params[c.SessionSecretParameter])
	c.gyazoAccessToken = strings.TrimSpace(params[c.GyazoAccessTokenParameter])
	for _, a := range c.Actors {
		if err := a.setPrivateKey([]byte(params[a.PrivateKeyParameter])); err != nil {
			return fmt.Errorf("actor %v: %w", a.Username, err)
		}
		a.apiToken = strings.TrimSpace(params[a.APITokenParameter])
	}
	return nil
}

func (a *ActorConfig) setPrivateKey(buf []byte) error {
	block, _ := pem.Decode(buf)
	if block == nil {
		return errors.New("invalid private key data")
	}
	key, err := parseRSAPrivateKey(block.Bytes)
	if err != nil {
		return err
	}
	a.privateKey = key

	// 公開鍵は秘密鍵から導出する。別ファイルや別パラメータで持つと、
	// 食い違ったままの公開鍵を actor に載せてしまう余地が残り、その場合
	// リモート側での署名検証が通らなくなる。導出しておけば構造的に
	// 起こり得ない。
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return err
	}
	a.publicKey = string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
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

// validate は Actors の整合性を確かめる。primary actor がちょうど1人、
// localpart の重複が無いこと、actor_type が有効な値であることを見る。
func (c *Config) validate() error {
	if len(c.Actors) == 0 {
		return errors.New("at least one actor is required")
	}
	primaryCount := 0
	seenLocalParts := map[string]bool{}
	for _, a := range c.Actors {
		if a.Primary {
			primaryCount++
		}
		lp := a.LocalPart()
		if seenLocalParts[lp] {
			return fmt.Errorf("duplicate actor localpart %q", lp)
		}
		seenLocalParts[lp] = true
		if a.ActorType != ActorTypePerson && a.ActorType != ActorTypeService {
			return fmt.Errorf("actor %v: actor_type must be %q or %q, got %q", a.Username, ActorTypePerson, ActorTypeService, a.ActorType)
		}
	}
	if primaryCount != 1 {
		return fmt.Errorf("exactly one actor must have primary: true, got %d", primaryCount)
	}
	return nil
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
	for _, a := range config.Actors {
		a.Origin = config.Origin
	}
	if err := config.validate(); err != nil {
		return nil, err
	}
	err = config.loadSecrets(ctx, region)
	return config, err
}
