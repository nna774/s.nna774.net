package config

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"strings"

	"gopkg.in/yaml.v2"
)

type Config struct {
	Username       string   `yaml:"username"`
	AliasUsernames []string `yaml:"alias_usernames"`
	Name           string   `yaml:"name"`
	Origin         string   `yaml:"origin"`
	IconURI        string   `yaml:"icon_uri"`
	PublicKeyName  string   `yaml:"public_key_name"`
	PublicKeyFile  string   `yaml:"public_key_file"`
	PrivateKeyFile string   `yaml:"private_key_file"`
}

func (c *Config) LocalPart() string {
	return strings.SplitN(c.Username, "@", 2)[0]
}

func (c *Config) ID() string {
	return c.Origin + "/u/" + c.LocalPart() // TODO: ? kimeuchi
}

func (c *Config) IconMediaType() string {
	return "image/jpeg" // TODO: detect from tail
}

func (c *Config) loadKeys() error {
	buf, err := os.ReadFile(c.PrivateKeyFile)
	if err != nil {
		return err
	}
	block, _ := pem.Decode(buf)
	if block == nil {
		return errors.New("invalid private key data")
	}
	privateKey, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return err
	}
	buf, err = os.ReadFile(c.PublicKeyFile)
	if err != nil {
		return err
	}
	publicKey = string(buf)
	return nil
}

var privateKey *rsa.PrivateKey
var publicKey string

func (c *Config) PrivateKey() *rsa.PrivateKey { return privateKey }
func (c *Config) PublicKey() string           { return publicKey }

func LoadConfig(configFile string) (*Config, error) {
	cfg, err := os.ReadFile(configFile)
	if err != nil {
		return nil, err
	}
	var config = &Config{}
	err = yaml.UnmarshalStrict(cfg, config)
	if err != nil {
		return nil, err
	}
	err = config.loadKeys()
	return config, err
}
