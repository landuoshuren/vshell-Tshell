package config

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	MasterType      string
	WebTitle        string
	WebPort         int
	WebIP           string
	BasicAuth       bool
	JWTSecret       string
	Username        string
	Password        string
	OpenSSL         bool
	CertFile        string
	KeyFile         string
	LogLevel        int
	LogPath         string
	DingdingToken   string
	DingdingKeyword string
	WxKey           string
}

func Default() Config {
	return Config{
		MasterType: "web", WebTitle: "管理平台", WebPort: 8082,
		WebIP: "0.0.0.0", Username: "admin", Password: "admin",
		JWTSecret: randomSecret(), LogLevel: 6,
	}
}

func Load(path string) (Config, error) {
	cfg := Default()
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}
	defer f.Close()
	values := map[string]string{}
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		values[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
	}
	if err := s.Err(); err != nil {
		return cfg, err
	}
	cfg.MasterType = get(values, "master_type", cfg.MasterType)
	cfg.WebTitle = get(values, "web_title", cfg.WebTitle)
	cfg.WebIP = get(values, "web_ip", cfg.WebIP)
	cfg.WebPort = getInt(values, "web_port", cfg.WebPort)
	cfg.BasicAuth = getBool(values, "web_basic_auth", cfg.BasicAuth)
	cfg.JWTSecret = get(values, "web_jwt_secret", cfg.JWTSecret)
	cfg.Username = get(values, "web_username", cfg.Username)
	cfg.Password = get(values, "web_password", cfg.Password)
	cfg.OpenSSL = getBool(values, "web_open_ssl", cfg.OpenSSL)
	cfg.CertFile = get(values, "web_cert_file", "conf/server.pem")
	cfg.KeyFile = get(values, "web_key_file", "conf/server.key")
	cfg.LogLevel = getInt(values, "log_level", cfg.LogLevel)
	cfg.LogPath = get(values, "log_path", cfg.LogPath)
	cfg.DingdingToken = get(values, "dingding_access_token", "")
	cfg.DingdingKeyword = get(values, "dingding_key_word", "")
	cfg.WxKey = get(values, "wx_key", "")
	return cfg, nil
}

func ResolveRunPath(explicit string) string {
	if explicit != "" {
		return explicit
	}
	exe, err := os.Executable()
	if err == nil {
		return filepath.Dir(exe)
	}
	wd, _ := os.Getwd()
	return wd
}

func get(m map[string]string, key, fallback string) string {
	if v, ok := m[key]; ok && v != "" {
		return v
	}
	return fallback
}
func getInt(m map[string]string, key string, fallback int) int {
	v, err := strconv.Atoi(m[key])
	if err != nil {
		return fallback
	}
	return v
}
func getBool(m map[string]string, key string, fallback bool) bool {
	v, err := strconv.ParseBool(m[key])
	if err != nil {
		return fallback
	}
	return v
}
func randomSecret() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
