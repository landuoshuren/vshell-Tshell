package protocol

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

const MaxFrameSize = 128 << 20

type Message struct {
	Type      string          `json:"type"`
	RequestID string          `json:"request_id,omitempty"`
	StreamID  string          `json:"stream_id,omitempty"`
	Action    string          `json:"action,omitempty"`
	OK        bool            `json:"ok,omitempty"`
	Error     string          `json:"error,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}

type Conn struct {
	Net net.Conn
	wmu sync.Mutex
}

func NewConn(c net.Conn) *Conn { return &Conn{Net: c} }

func (c *Conn) ReadMessage() (Message, error) {
	var m Message
	var n uint32
	if err := binary.Read(c.Net, binary.BigEndian, &n); err != nil {
		return m, err
	}
	if n == 0 || n > MaxFrameSize {
		return m, errors.New("invalid frame size")
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(c.Net, b); err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, err
	}
	return m, nil
}

func (c *Conn) WriteMessage(m Message) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if err := binary.Write(c.Net, binary.BigEndian, uint32(len(b))); err != nil {
		return err
	}
	_, err = c.Net.Write(b)
	return err
}

func (c *Conn) SetDeadline(t time.Time) error { return c.Net.SetDeadline(t) }
func (c *Conn) Close() error                  { return c.Net.Close() }

func MarshalData(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
