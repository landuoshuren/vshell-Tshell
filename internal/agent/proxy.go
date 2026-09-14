package agent

import (
	"bufio"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	xproxy "golang.org/x/net/proxy"
)

func dialProxyTCP(proxyAddress, target string) (net.Conn, error) {
	if strings.TrimSpace(proxyAddress) == "" {
		return net.DialTimeout("tcp", target, 15*time.Second)
	}
	u, err := url.Parse(proxyAddress)
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(u.Scheme) {
	case "socks5", "socks5h":
		var auth *xproxy.Auth
		if u.User != nil {
			password, _ := u.User.Password()
			auth = &xproxy.Auth{User: u.User.Username(), Password: password}
		}
		dialer, err := xproxy.SOCKS5("tcp", u.Host, auth, &net.Dialer{Timeout: 15 * time.Second})
		if err != nil {
			return nil, err
		}
		return dialer.Dial("tcp", target)
	case "http", "https":
		return dialHTTPProxy(u, target)
	default:
		return nil, fmt.Errorf("unsupported proxy scheme %q", u.Scheme)
	}
}

func dialHTTPProxy(proxyURL *url.URL, target string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	var conn net.Conn
	var err error
	if strings.EqualFold(proxyURL.Scheme, "https") {
		host, _, splitErr := net.SplitHostPort(proxyURL.Host)
		if splitErr != nil {
			host = proxyURL.Host
		}
		conn, err = tls.DialWithDialer(dialer, "tcp", proxyURL.Host, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	} else {
		conn, err = dialer.Dial("tcp", proxyURL.Host)
	}
	if err != nil {
		return nil, err
	}
	request := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: target}, Host: target, Header: make(http.Header)}
	if proxyURL.User != nil {
		password, _ := proxyURL.User.Password()
		request.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(proxyURL.User.Username()+":"+password)))
	}
	if err = request.Write(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, request)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		if response.Body != nil {
			_ = response.Body.Close()
		}
		_ = conn.Close()
		return nil, errors.New(response.Status)
	}
	return &proxyBufferedConn{Conn: conn, reader: reader}, nil
}

type proxyBufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *proxyBufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }
