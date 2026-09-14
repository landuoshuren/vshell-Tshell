package shellcode

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/Binject/go-donut/donut"
)

func FromPE(path, arch, format string) ([]byte, string, error) {
	return FromPEWithParameters(path, arch, "", format)
}

func FromPEWithParameters(path, arch, parameters, format string) ([]byte, string, error) {
	cfg := donut.DefaultConfig()
	cfg.Type = donut.DONUT_MODULE_EXE
	cfg.Parameters = parameters
	return fromFile(path, arch, format, cfg)
}

func FromDLL(path, arch, method, format string) ([]byte, string, error) {
	cfg := donut.DefaultConfig()
	cfg.Type = donut.DONUT_MODULE_DLL
	cfg.Method = method
	return fromFile(path, arch, format, cfg)
}

func fromFile(path, arch, format string, cfg *donut.DonutConfig) ([]byte, string, error) {
	cfg.InstType = donut.DONUT_INSTANCE_PIC
	cfg.Entropy = donut.DONUT_ENTROPY_NONE
	cfg.Bypass = 0
	cfg.Thread = 0
	if strings.Contains(arch, "i386") {
		cfg.Arch = donut.X32
	} else {
		cfg.Arch = donut.X64
	}
	buf, err := donut.ShellcodeFromFile(path, cfg)
	if err != nil {
		return nil, "", err
	}
	raw := buf.Bytes()
	switch format {
	case ".c":
		return asC(raw), "text/x-c; charset=utf-8", nil
	case ".raw.txt":
		return []byte(asEscaped(raw)), "text/plain; charset=utf-8", nil
	default:
		return append([]byte(nil), raw...), "application/octet-stream", nil
	}
}

func asEscaped(data []byte) string {
	var out strings.Builder
	out.Grow(len(data) * 4)
	for _, b := range data {
		_, _ = fmt.Fprintf(&out, "\\x%02x", b)
	}
	return out.String()
}

func asC(data []byte) []byte {
	var out bytes.Buffer
	_, _ = fmt.Fprintf(&out, "unsigned char vshell_shellcode[%d] = {\n", len(data))
	for i, b := range data {
		if i%16 == 0 {
			_, _ = out.WriteString("  ")
		}
		_, _ = fmt.Fprintf(&out, "0x%02x", b)
		if i+1 != len(data) {
			_, _ = out.WriteString(",")
		}
		if i%16 == 15 || i+1 == len(data) {
			_, _ = out.WriteString("\n")
		} else {
			_ = out.WriteByte(' ')
		}
	}
	_, _ = out.WriteString("};\n")
	return out.Bytes()
}
