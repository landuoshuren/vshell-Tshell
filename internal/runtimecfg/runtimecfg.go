package runtimecfg

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
)

var magic = [8]byte{'V', 'S', 'H', 'C', 'F', 'G', '0', '1'}

const trailerSize = 16

type Config struct {
	ServerAddr string `json:"server_addr"`
	Vkey       string `json:"vkey"`
	VerifyKey  string `json:"verify_key"`
	Transport  string `json:"transport"`
	DNSDomain  string `json:"dns_domain,omitempty"`
	PublicDNS  string `json:"public_dns,omitempty"`
	OSSURL     string `json:"oss_url,omitempty"`
	OSSKey     string `json:"oss_key,omitempty"`
	ProxyURL   string `json:"proxy_url,omitempty"`
	Salt       string `json:"salt,omitempty"`
}

func Append(program []byte, cfg Config) ([]byte, error) {
	payload, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(program)+len(payload)+trailerSize)
	out = append(out, program...)
	out = append(out, payload...)
	out = append(out, magic[:]...)
	var length [8]byte
	binary.LittleEndian.PutUint64(length[:], uint64(len(payload)))
	out = append(out, length[:]...)
	return out, nil
}

func Encode(cfg Config) (string, error) {
	payload, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func Decode(encoded string) (Config, error) {
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(payload, &cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func Parse(program []byte) (Config, error) {
	var cfg Config
	if len(program) < trailerSize {
		return cfg, errors.New("embedded configuration not present")
	}
	trailer := program[len(program)-trailerSize:]
	if !bytes.Equal(trailer[:8], magic[:]) {
		return cfg, errors.New("embedded configuration not present")
	}
	length := binary.LittleEndian.Uint64(trailer[8:])
	if length == 0 || length > uint64(len(program)-trailerSize) {
		return cfg, errors.New("invalid embedded configuration")
	}
	start := len(program) - trailerSize - int(length)
	if err := json.Unmarshal(program[start:len(program)-trailerSize], &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func LoadExecutable() (Config, error) {
	path, err := os.Executable()
	if err != nil {
		return Config{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	return Parse(data)
}
