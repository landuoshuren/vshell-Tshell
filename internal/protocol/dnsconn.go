package protocol

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/base32"
	"encoding/binary"
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

	kcp "github.com/xtaci/kcp-go/v5"
)

const dnsTunnelMTU = 100

type dnsSessionAddr struct{ id [8]byte }

func (a dnsSessionAddr) Network() string { return "dns" }
func (a dnsSessionAddr) String() string  { return hex.EncodeToString(a.id[:]) }

type dnsDatagram struct {
	b    []byte
	addr net.Addr
}

type dnsServerPacketConn struct {
	conn      *net.UDPConn
	domain    string
	incoming  chan dnsDatagram
	closed    chan struct{}
	closeOnce sync.Once
	mu        sync.Mutex
	outgoing  map[[8]byte]chan []byte
	readDL    time.Time
	writeDL   time.Time
}

func ListenDNSTunnel(listenAddr, domain string) (net.Listener, error) {
	udpAddr, err := net.ResolveUDPAddr("udp", listenAddr)
	if err != nil {
		return nil, err
	}
	udp, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return nil, err
	}
	pc := &dnsServerPacketConn{
		conn: udp, domain: cleanDomain(domain), incoming: make(chan dnsDatagram, 2048),
		closed: make(chan struct{}), outgoing: make(map[[8]byte]chan []byte),
	}
	go pc.readQueries()
	ln, err := kcp.ServeConn(nil, 0, 0, pc)
	if err != nil {
		_ = pc.Close()
		return nil, err
	}
	return ln, nil
}

func DialDNSTunnel(mode, resolver, domain string) (net.Conn, error) {
	if resolver == "" {
		return nil, errors.New("public dns address is empty")
	}
	if cleanDomain(domain) == "" {
		return nil, errors.New("dns domain is empty")
	}
	pc := newDNSClientPacketConn(mode, resolver, domain)
	session, err := kcp.NewConn2(pc.remote, nil, 0, 0, pc)
	if err != nil {
		_ = pc.Close()
		return nil, err
	}
	tuneDNSKCP(session)
	return session, nil
}

func TuneDNSConn(conn *kcp.UDPSession) { tuneDNSKCP(conn) }

func tuneDNSKCP(session *kcp.UDPSession) {
	session.SetStreamMode(true)
	session.SetWriteDelay(true)
	session.SetNoDelay(1, 20, 2, 1)
	session.SetWindowSize(256, 256)
	_ = session.SetMtu(dnsTunnelMTU)
	session.SetACKNoDelay(true)
}

func (p *dnsServerPacketConn) sessionQueue(id [8]byte) chan []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	q := p.outgoing[id]
	if q == nil {
		q = make(chan []byte, 2048)
		p.outgoing[id] = q
	}
	return q
}

func (p *dnsServerPacketConn) readQueries() {
	buf := make([]byte, 4096)
	for {
		n, remote, err := p.conn.ReadFromUDP(buf)
		if err != nil {
			p.closeOnce.Do(func() { close(p.closed) })
			return
		}
		packet := append([]byte(nil), buf[:n]...)
		go p.handleQuery(packet, remote)
	}
}

func (p *dnsServerPacketConn) handleQuery(query []byte, remote *net.UDPAddr) {
	id, question, session, payload, ok := parseTunnelQuery(query, p.domain)
	if !ok {
		return
	}
	q := p.sessionQueue(session)
	if len(payload) != 0 {
		select {
		case p.incoming <- dnsDatagram{b: payload, addr: dnsSessionAddr{id: session}}:
		case <-p.closed:
			return
		}
	}
	var responsePayload []byte
	select {
	case responsePayload = <-q:
	case <-time.After(35 * time.Millisecond):
	case <-p.closed:
		return
	}
	response := buildTXTResponse(id, question, responsePayload)
	_, _ = p.conn.WriteToUDP(response, remote)
}

func (p *dnsServerPacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	timer, stop := deadlineTimer(p.readDeadline())
	defer stop()
	select {
	case item := <-p.incoming:
		return copy(b, item.b), item.addr, nil
	case <-p.closed:
		return 0, nil, net.ErrClosed
	case <-timer:
		return 0, nil, timeoutError("read")
	}
}

func (p *dnsServerPacketConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	dnsAddr, ok := addr.(dnsSessionAddr)
	if !ok {
		if ptr, ptrOK := addr.(*dnsSessionAddr); ptrOK {
			dnsAddr = *ptr
			ok = true
		}
	}
	if !ok {
		return 0, fmt.Errorf("unexpected dns session address %T", addr)
	}
	data := append([]byte(nil), b...)
	timer, stop := deadlineTimer(p.writeDeadline())
	defer stop()
	select {
	case p.sessionQueue(dnsAddr.id) <- data:
		return len(b), nil
	case <-p.closed:
		return 0, net.ErrClosed
	case <-timer:
		return 0, timeoutError("write")
	}
}

func (p *dnsServerPacketConn) Close() error {
	p.closeOnce.Do(func() { close(p.closed) })
	return p.conn.Close()
}
func (p *dnsServerPacketConn) LocalAddr() net.Addr { return p.conn.LocalAddr() }
func (p *dnsServerPacketConn) SetDeadline(t time.Time) error {
	p.mu.Lock()
	p.readDL, p.writeDL = t, t
	p.mu.Unlock()
	return nil
}
func (p *dnsServerPacketConn) SetReadDeadline(t time.Time) error {
	p.mu.Lock()
	p.readDL = t
	p.mu.Unlock()
	return nil
}
func (p *dnsServerPacketConn) SetWriteDeadline(t time.Time) error {
	p.mu.Lock()
	p.writeDL = t
	p.mu.Unlock()
	return nil
}
func (p *dnsServerPacketConn) readDeadline() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.readDL
}
func (p *dnsServerPacketConn) writeDeadline() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.writeDL
}

type dnsClientPacketConn struct {
	mode      string
	resolver  string
	domain    string
	session   [8]byte
	remote    dnsSessionAddr
	incoming  chan []byte
	closed    chan struct{}
	closeOnce sync.Once
	mu        sync.Mutex
	readDL    time.Time
	writeDL   time.Time
	sem       chan struct{}
	udpMu     sync.Mutex
	udpConn   net.Conn
}

func newDNSClientPacketConn(mode, resolver, domain string) *dnsClientPacketConn {
	p := &dnsClientPacketConn{mode: strings.ToLower(mode), resolver: resolver, domain: cleanDomain(domain), incoming: make(chan []byte, 2048), closed: make(chan struct{}), sem: make(chan struct{}, 8)}
	_, _ = rand.Read(p.session[:])
	p.remote = dnsSessionAddr{id: p.session}
	go p.poll()
	return p
}

func (p *dnsClientPacketConn) poll() {
	ticker := time.NewTicker(70 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			probe := make([]byte, 17)
			copy(probe, p.session[:])
			probe[8] = 0xe8
			_, _ = rand.Read(probe[9:])
			go p.exchange(probe)
		case <-p.closed:
			return
		}
	}
}

func (p *dnsClientPacketConn) exchange(packet []byte) {
	select {
	case p.sem <- struct{}{}:
	case <-p.closed:
		return
	}
	defer func() { <-p.sem }()
	query, err := buildTunnelQuery(packet, p.domain)
	if err != nil {
		return
	}
	response, err := p.exchangeWire(query)
	if err != nil {
		return
	}
	payload, ok := parseTXTResponse(response)
	if !ok || len(payload) == 0 {
		return
	}
	select {
	case p.incoming <- payload:
	case <-p.closed:
	}
}

func (p *dnsClientPacketConn) exchangeWire(query []byte) ([]byte, error) {
	switch p.mode {
	case "doh":
		req, err := http.NewRequest(http.MethodPost, p.resolver, strings.NewReader(string(query)))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/dns-message")
		req.Header.Set("Accept", "application/dns-message")
		client := &http.Client{Timeout: 8 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return nil, fmt.Errorf("doh status %s", resp.Status)
		}
		return io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	case "dot":
		address := ensurePort(p.resolver, "853")
		host, _, _ := net.SplitHostPort(address)
		dialer := &net.Dialer{Timeout: 8 * time.Second}
		conn, err := tls.DialWithDialer(dialer, "tcp", address, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
		if err != nil {
			return nil, err
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
		if err = binary.Write(conn, binary.BigEndian, uint16(len(query))); err != nil {
			return nil, err
		}
		if _, err = conn.Write(query); err != nil {
			return nil, err
		}
		var n uint16
		if err = binary.Read(conn, binary.BigEndian, &n); err != nil {
			return nil, err
		}
		response := make([]byte, n)
		_, err = io.ReadFull(conn, response)
		return response, err
	default:
		address := ensurePort(p.resolver, "53")
		p.udpMu.Lock()
		defer p.udpMu.Unlock()
		if p.udpConn == nil {
			conn, err := net.DialTimeout("udp", address, 5*time.Second)
			if err != nil {
				return nil, err
			}
			p.udpConn = conn
		}
		_ = p.udpConn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := p.udpConn.Write(query); err != nil {
			_ = p.udpConn.Close()
			p.udpConn = nil
			return nil, err
		}
		response := make([]byte, 4096)
		n, err := p.udpConn.Read(response)
		if err != nil {
			_ = p.udpConn.Close()
			p.udpConn = nil
		}
		return response[:n], err
	}
}

func (p *dnsClientPacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	timer, stop := deadlineTimer(p.readDeadline())
	defer stop()
	select {
	case payload := <-p.incoming:
		return copy(b, payload), p.remote, nil
	case <-p.closed:
		return 0, nil, net.ErrClosed
	case <-timer:
		return 0, nil, timeoutError("read")
	}
}

func (p *dnsClientPacketConn) WriteTo(b []byte, _ net.Addr) (int, error) {
	packet := make([]byte, 12+len(b))
	copy(packet, p.session[:])
	packet[8] = 0xe3
	_, _ = rand.Read(packet[9:12])
	copy(packet[12:], b)
	go p.exchange(packet)
	return len(b), nil
}

func (p *dnsClientPacketConn) Close() error {
	p.closeOnce.Do(func() {
		close(p.closed)
		p.udpMu.Lock()
		if p.udpConn != nil {
			_ = p.udpConn.Close()
			p.udpConn = nil
		}
		p.udpMu.Unlock()
	})
	return nil
}
func (p *dnsClientPacketConn) LocalAddr() net.Addr { return dnsSessionAddr{id: p.session} }
func (p *dnsClientPacketConn) SetDeadline(t time.Time) error {
	p.mu.Lock()
	p.readDL, p.writeDL = t, t
	p.mu.Unlock()
	return nil
}
func (p *dnsClientPacketConn) SetReadDeadline(t time.Time) error {
	p.mu.Lock()
	p.readDL = t
	p.mu.Unlock()
	return nil
}
func (p *dnsClientPacketConn) SetWriteDeadline(t time.Time) error {
	p.mu.Lock()
	p.writeDL = t
	p.mu.Unlock()
	return nil
}
func (p *dnsClientPacketConn) readDeadline() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.readDL
}

type dnsTimeoutError string

func (e dnsTimeoutError) Error() string   { return string(e) + " timeout" }
func (e dnsTimeoutError) Timeout() bool   { return true }
func (e dnsTimeoutError) Temporary() bool { return true }
func timeoutError(op string) error        { return dnsTimeoutError(op) }

func deadlineTimer(deadline time.Time) (<-chan time.Time, func()) {
	if deadline.IsZero() {
		return make(chan time.Time), func() {}
	}
	d := time.Until(deadline)
	if d < 0 {
		d = 0
	}
	t := time.NewTimer(d)
	return t.C, func() { t.Stop() }
}

func cleanDomain(domain string) string { return strings.Trim(strings.TrimSpace(domain), ".") }

func ensurePort(address, fallback string) string {
	if parsed, err := url.Parse(address); err == nil && parsed.Host != "" {
		address = parsed.Host
	}
	if _, _, err := net.SplitHostPort(address); err == nil {
		return address
	}
	return net.JoinHostPort(strings.Trim(address, "[]"), fallback)
}

func buildTunnelQuery(payload []byte, domain string) ([]byte, error) {
	encoded := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(payload))
	labels := make([]string, 0, len(encoded)/63+8)
	for len(encoded) > 0 {
		n := 63
		if len(encoded) < n {
			n = len(encoded)
		}
		labels = append(labels, encoded[:n])
		encoded = encoded[n:]
	}
	if domain != "" {
		labels = append(labels, strings.Split(cleanDomain(domain), ".")...)
	}
	qname, err := encodeDNSName(labels)
	if err != nil {
		return nil, err
	}
	msg := make([]byte, 12, 12+len(qname)+15)
	var id [2]byte
	_, _ = rand.Read(id[:])
	copy(msg[:2], id[:])
	binary.BigEndian.PutUint16(msg[2:4], 0x0100)
	binary.BigEndian.PutUint16(msg[4:6], 1)
	binary.BigEndian.PutUint16(msg[10:12], 1)
	msg = append(msg, qname...)
	msg = append(msg, 0, 16, 0, 1)
	msg = append(msg, 0, 0, 41, 0x10, 0, 0, 0, 0, 0, 0)
	return msg, nil
}

func parseTunnelQuery(msg []byte, domain string) (uint16, []byte, [8]byte, []byte, bool) {
	var session [8]byte
	if len(msg) < 17 || binary.BigEndian.Uint16(msg[4:6]) != 1 {
		return 0, nil, session, nil, false
	}
	labels, end, ok := decodeDNSName(msg, 12)
	if !ok || end+4 > len(msg) || binary.BigEndian.Uint16(msg[end:end+2]) != 16 {
		return 0, nil, session, nil, false
	}
	domainLabels := strings.Split(cleanDomain(domain), ".")
	if len(domainLabels) > len(labels) {
		return 0, nil, session, nil, false
	}
	for i := range domainLabels {
		if !strings.EqualFold(labels[len(labels)-len(domainLabels)+i], domainLabels[i]) {
			return 0, nil, session, nil, false
		}
	}
	encoded := strings.Join(labels[:len(labels)-len(domainLabels)], "")
	raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(encoded))
	if err != nil || len(raw) < 9 {
		return 0, nil, session, nil, false
	}
	copy(session[:], raw[:8])
	var payload []byte
	if raw[8] == 0xe3 && len(raw) >= 12 {
		payload = append([]byte(nil), raw[12:]...)
	}
	question := append([]byte(nil), msg[12:end+4]...)
	return binary.BigEndian.Uint16(msg[:2]), question, session, payload, true
}

func buildTXTResponse(id uint16, question, payload []byte) []byte {
	content := []byte{0}
	if len(payload) != 0 {
		content = make([]byte, 2+len(payload))
		binary.BigEndian.PutUint16(content[:2], uint16(len(payload)))
		copy(content[2:], payload)
	}
	rdata := make([]byte, 0, len(content)+len(content)/255+1)
	for len(content) > 0 {
		n := 255
		if len(content) < n {
			n = len(content)
		}
		rdata = append(rdata, byte(n))
		rdata = append(rdata, content[:n]...)
		content = content[n:]
	}
	msg := make([]byte, 12, 12+len(question)+12+len(rdata)+11)
	binary.BigEndian.PutUint16(msg[:2], id)
	binary.BigEndian.PutUint16(msg[2:4], 0x8400)
	binary.BigEndian.PutUint16(msg[4:6], 1)
	binary.BigEndian.PutUint16(msg[6:8], 1)
	binary.BigEndian.PutUint16(msg[10:12], 1)
	msg = append(msg, question...)
	msg = append(msg, 0xc0, 0x0c, 0, 16, 0, 1, 0, 0, 0, 60)
	rdlen := make([]byte, 2)
	binary.BigEndian.PutUint16(rdlen, uint16(len(rdata)))
	msg = append(msg, rdlen...)
	msg = append(msg, rdata...)
	msg = append(msg, 0, 0, 41, 0x10, 0, 0, 0, 0, 0, 0)
	return msg
}

func parseTXTResponse(msg []byte) ([]byte, bool) {
	if len(msg) < 12 || binary.BigEndian.Uint16(msg[6:8]) == 0 {
		return nil, false
	}
	off := 12
	_, off, ok := decodeDNSName(msg, off)
	if !ok || off+4 > len(msg) {
		return nil, false
	}
	off += 4
	_, off, ok = decodeDNSName(msg, off)
	if !ok || off+10 > len(msg) || binary.BigEndian.Uint16(msg[off:off+2]) != 16 {
		return nil, false
	}
	rdlen := int(binary.BigEndian.Uint16(msg[off+8 : off+10]))
	off += 10
	if off+rdlen > len(msg) {
		return nil, false
	}
	end := off + rdlen
	var content []byte
	for off < end {
		n := int(msg[off])
		off++
		if off+n > end {
			return nil, false
		}
		content = append(content, msg[off:off+n]...)
		off += n
	}
	if len(content) == 1 && content[0] == 0 {
		return nil, true
	}
	if len(content) < 2 {
		return nil, false
	}
	n := int(binary.BigEndian.Uint16(content[:2]))
	if n > len(content)-2 {
		return nil, false
	}
	return append([]byte(nil), content[2:2+n]...), true
}

func encodeDNSName(labels []string) ([]byte, error) {
	var out []byte
	for _, label := range labels {
		if label == "" || len(label) > 63 {
			return nil, errors.New("invalid dns label")
		}
		out = append(out, byte(len(label)))
		out = append(out, label...)
	}
	out = append(out, 0)
	if len(out) > 255 {
		return nil, errors.New("dns name is too long")
	}
	return out, nil
}

func decodeDNSName(msg []byte, offset int) ([]string, int, bool) {
	labels := []string{}
	next := -1
	for jumps := 0; jumps < 16; jumps++ {
		if offset >= len(msg) {
			return nil, 0, false
		}
		n := int(msg[offset])
		if n == 0 {
			offset++
			if next >= 0 {
				offset = next
			}
			return labels, offset, true
		}
		if n&0xc0 == 0xc0 {
			if offset+1 >= len(msg) {
				return nil, 0, false
			}
			if next < 0 {
				next = offset + 2
			}
			offset = int(binary.BigEndian.Uint16(msg[offset:offset+2]) & 0x3fff)
			continue
		}
		offset++
		if n > 63 || offset+n > len(msg) {
			return nil, 0, false
		}
		labels = append(labels, string(msg[offset:offset+n]))
		offset += n
	}
	return nil, 0, false
}
