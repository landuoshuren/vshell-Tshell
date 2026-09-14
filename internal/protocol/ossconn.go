package protocol

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const ossPollInterval = 20 * time.Millisecond

var ossDialLocks sync.Map

type ossAddress string

func (a ossAddress) Network() string { return "oss" }
func (a ossAddress) String() string  { return string(a) }

type ossListener struct {
	baseURL string
	key     string
	client  *http.Client
	accept  chan net.Conn
	closed  chan struct{}
	once    sync.Once
}

func ListenOSSTunnel(baseURL, key string) (net.Listener, error) {
	if err := validateOSSArguments(baseURL, key); err != nil {
		return nil, err
	}
	l := &ossListener{
		baseURL: strings.TrimRight(baseURL, "/"),
		key:     key,
		client:  newOSSHTTPClient(),
		accept:  make(chan net.Conn, 16),
		closed:  make(chan struct{}),
	}
	_ = ossDelete(l.client, l.objectURL("r"))
	_ = ossDelete(l.client, l.objectURL("w"))
	go l.poll()
	return l, nil
}

func (l *ossListener) objectURL(suffix string) string {
	return l.baseURL + "/" + l.key + suffix + ".jpg"
}

func (l *ossListener) poll() {
	for {
		select {
		case <-l.closed:
			return
		default:
		}
		address, status, err := ossGet(l.client, l.objectURL("w"))
		if err != nil || status == http.StatusNotFound || len(address) == 0 {
			if !waitOSS(l.closed, ossPollInterval) {
				return
			}
			continue
		}
		if status != http.StatusOK {
			if !waitOSS(l.closed, 250*time.Millisecond) {
				return
			}
			continue
		}
		peer := strings.TrimSpace(string(address))
		if peer == "" {
			_ = ossDelete(l.client, l.objectURL("w"))
			continue
		}
		_ = ossDelete(l.client, l.objectURL("w"))
		if err := ossPut(l.client, l.objectURL("r"), []byte(peer+"|sso|ok")); err != nil {
			continue
		}
		channel := ossChannel(peer)
		conn := newOSSConn(l.baseURL, channel, false, peer, l.client)
		select {
		case l.accept <- conn:
		case <-l.closed:
			_ = conn.Close()
			return
		}
	}
}

func (l *ossListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.accept:
		return conn, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *ossListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *ossListener) Addr() net.Addr { return ossAddress(l.baseURL) }

func DialOSSTunnel(baseURL, key string) (net.Conn, error) {
	if err := validateOSSArguments(baseURL, key); err != nil {
		return nil, err
	}
	baseURL = strings.TrimRight(baseURL, "/")
	client := newOSSHTTPClient()
	if _, _, err := ossGet(client, baseURL); err != nil {
		return nil, err
	}
	lockValue, _ := ossDialLocks.LoadOrStore(baseURL+"|"+key, &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	peer := ossLocalAddress(baseURL)
	readURL := baseURL + "/" + key + "r.jpg"
	writeURL := baseURL + "/" + key + "w.jpg"
	_ = ossDelete(client, readURL)
	if err := ossPut(client, writeURL, []byte(peer)); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		body, status, err := ossGet(client, readURL)
		if err == nil && status == http.StatusOK && string(body) == peer+"|sso|ok" {
			_ = ossDelete(client, readURL)
			return newOSSConn(baseURL, ossChannel(peer), true, peer, client), nil
		}
		time.Sleep(ossPollInterval)
	}
	return nil, errors.New("oss listener handshake timeout")
}

func validateOSSArguments(baseURL, key string) error {
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return errors.New("invalid oss url")
	}
	if len(key) != 32 {
		return errors.New("invalid oss key")
	}
	return nil
}

func ossChannel(peer string) string {
	sum := md5.Sum([]byte(peer))
	return hex.EncodeToString(sum[:])
}

func ossLocalAddress(baseURL string) string {
	parsed, err := url.Parse(baseURL)
	if err == nil {
		host := parsed.Host
		if !strings.Contains(host, ":") {
			if parsed.Scheme == "https" {
				host += ":443"
			} else {
				host += ":80"
			}
		}
		if conn, dialErr := net.DialTimeout("tcp", host, 3*time.Second); dialErr == nil {
			address := conn.LocalAddr().String()
			_ = conn.Close()
			return address
		}
	}
	return fmt.Sprintf("127.0.0.1:%d", time.Now().UnixNano()%50000+10000)
}

type ossConn struct {
	baseURL       string
	channel       string
	client        *http.Client
	readURL       string
	writeURL      string
	localAddress  net.Addr
	peerAddress   net.Addr
	readMu        sync.Mutex
	writeMu       sync.Mutex
	stateMu       sync.RWMutex
	readBuffer    []byte
	readDeadline  time.Time
	writeDeadline time.Time
	closed        chan struct{}
	once          sync.Once
}

func newOSSConn(baseURL, channel string, clientSide bool, peer string, client *http.Client) *ossConn {
	readSuffix, writeSuffix := "w", "r"
	local, remote := net.Addr(ossAddress(baseURL)), net.Addr(ossAddress(peer))
	if clientSide {
		readSuffix, writeSuffix = "r", "w"
		local, remote = ossAddress(peer), ossAddress(baseURL)
	}
	return &ossConn{
		baseURL: baseURL, channel: channel, client: client,
		readURL:      baseURL + "/" + channel + readSuffix + ".jpg",
		writeURL:     baseURL + "/" + channel + writeSuffix + ".jpg",
		localAddress: local, peerAddress: remote, closed: make(chan struct{}),
	}
}

func (c *ossConn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	c.readMu.Lock()
	defer c.readMu.Unlock()
	for len(c.readBuffer) == 0 {
		if c.isClosed() {
			return 0, net.ErrClosed
		}
		deadline, _ := c.deadlines()
		if !deadline.IsZero() && time.Now().After(deadline) {
			return 0, ossDeadlineError("read")
		}
		body, status, err := ossGet(c.client, c.readURL)
		if err != nil {
			return 0, err
		}
		if status == http.StatusOK {
			_ = ossDelete(c.client, c.readURL)
			if len(body) != 0 {
				c.readBuffer = body
				break
			}
		}
		if !waitOSS(c.closed, ossPollInterval) {
			return 0, net.ErrClosed
		}
	}
	n := copy(p, c.readBuffer)
	c.readBuffer = c.readBuffer[n:]
	return n, nil
}

func (c *ossConn) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	for {
		if c.isClosed() {
			return 0, net.ErrClosed
		}
		_, deadline := c.deadlines()
		if !deadline.IsZero() && time.Now().After(deadline) {
			return 0, ossDeadlineError("write")
		}
		status, err := ossHead(c.client, c.writeURL)
		if err != nil {
			return 0, err
		}
		if status == http.StatusNotFound {
			break
		}
		if !waitOSS(c.closed, ossPollInterval) {
			return 0, net.ErrClosed
		}
	}
	if err := ossPut(c.client, c.writeURL, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *ossConn) Close() error {
	c.once.Do(func() {
		close(c.closed)
		_ = ossDelete(c.client, c.readURL)
		_ = ossDelete(c.client, c.writeURL)
	})
	return nil
}

func (c *ossConn) LocalAddr() net.Addr  { return c.localAddress }
func (c *ossConn) RemoteAddr() net.Addr { return c.peerAddress }

func (c *ossConn) SetDeadline(t time.Time) error {
	c.stateMu.Lock()
	c.readDeadline, c.writeDeadline = t, t
	c.stateMu.Unlock()
	return nil
}

func (c *ossConn) SetReadDeadline(t time.Time) error {
	c.stateMu.Lock()
	c.readDeadline = t
	c.stateMu.Unlock()
	return nil
}

func (c *ossConn) SetWriteDeadline(t time.Time) error {
	c.stateMu.Lock()
	c.writeDeadline = t
	c.stateMu.Unlock()
	return nil
}

func (c *ossConn) deadlines() (time.Time, time.Time) {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.readDeadline, c.writeDeadline
}

func (c *ossConn) isClosed() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

type ossTimeoutError string

func ossDeadlineError(operation string) error { return ossTimeoutError(operation + " timeout") }
func (e ossTimeoutError) Error() string       { return string(e) }
func (e ossTimeoutError) Timeout() bool       { return true }
func (e ossTimeoutError) Temporary() bool     { return true }

func newOSSHTTPClient() *http.Client {
	transport := &http.Transport{
		Proxy:        http.ProxyFromEnvironment,
		DialContext:  (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns: 32, MaxIdleConnsPerHost: 16, IdleConnTimeout: 60 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
	}
	return &http.Client{Transport: transport, Timeout: 30 * time.Second}
}

func ossGet(client *http.Client, objectURL string) ([]byte, int, error) {
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, objectURL, nil)
	if err != nil {
		return nil, 0, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, MaxFrameSize+1))
	if err != nil {
		return nil, response.StatusCode, err
	}
	if len(body) > MaxFrameSize {
		return nil, response.StatusCode, errors.New("oss object is too large")
	}
	return body, response.StatusCode, nil
}

func ossHead(client *http.Client, objectURL string) (int, error) {
	request, err := http.NewRequestWithContext(context.Background(), http.MethodHead, objectURL, nil)
	if err != nil {
		return 0, err
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	_ = response.Body.Close()
	return response.StatusCode, nil
}

func ossPut(client *http.Client, objectURL string, data []byte) error {
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPut, objectURL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("oss put returned %s", response.Status)
	}
	return nil
}

func ossDelete(client *http.Client, objectURL string) error {
	request, err := http.NewRequestWithContext(context.Background(), http.MethodDelete, objectURL, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	return nil
}

func waitOSS(closed <-chan struct{}, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-closed:
		return false
	case <-timer.C:
		return true
	}
}
