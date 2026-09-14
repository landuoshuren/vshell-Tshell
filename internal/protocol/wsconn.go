package protocol

import (
	"io"
	"net"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// WebSocketConn presents websocket binary messages as a continuous net.Conn
// byte stream so the same framed vShell protocol can run over TCP and WS.
type WebSocketConn struct {
	ws      *websocket.Conn
	readMu  sync.Mutex
	writeMu sync.Mutex
	reader  io.Reader
}

func NewWebSocketConn(ws *websocket.Conn) net.Conn { return &WebSocketConn{ws: ws} }

func (c *WebSocketConn) Read(p []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	for {
		if c.reader == nil {
			messageType, reader, err := c.ws.NextReader()
			if err != nil {
				return 0, err
			}
			if messageType != websocket.BinaryMessage && messageType != websocket.TextMessage {
				continue
			}
			c.reader = reader
		}
		n, err := c.reader.Read(p)
		if err == io.EOF {
			c.reader = nil
			if n != 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (c *WebSocketConn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.ws.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *WebSocketConn) Close() error         { return c.ws.Close() }
func (c *WebSocketConn) LocalAddr() net.Addr  { return c.ws.LocalAddr() }
func (c *WebSocketConn) RemoteAddr() net.Addr { return c.ws.RemoteAddr() }
func (c *WebSocketConn) SetDeadline(t time.Time) error {
	return firstError(c.ws.SetReadDeadline(t), c.ws.SetWriteDeadline(t))
}
func (c *WebSocketConn) SetReadDeadline(t time.Time) error  { return c.ws.SetReadDeadline(t) }
func (c *WebSocketConn) SetWriteDeadline(t time.Time) error { return c.ws.SetWriteDeadline(t) }

func firstError(a, b error) error {
	if a != nil {
		return a
	}
	return b
}
