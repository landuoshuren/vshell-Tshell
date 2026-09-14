package protocol

import (
	"bytes"
	"crypto/rand"
	"io"
	"net"
	"testing"
	"time"
)

func TestDNSPacketConnRoundTrip(t *testing.T) {
	udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	server := &dnsServerPacketConn{conn: udp, domain: "ns.vshell.test", incoming: make(chan dnsDatagram, 8), closed: make(chan struct{}), outgoing: make(map[[8]byte]chan []byte)}
	go server.readQueries()
	defer server.Close()
	client := newDNSClientPacketConn("dns", udp.LocalAddr().String(), "ns.vshell.test")
	defer client.Close()
	payload := []byte("packet-conn-probe")
	if _, err = client.WriteTo(payload, client.remote); err != nil {
		t.Fatal(err)
	}
	_ = server.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 200)
	n, addr, err := server.ReadFrom(buf)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf[:n], payload) {
		t.Fatal("server payload mismatch")
	}
	if _, err = server.WriteTo(append([]byte("reply:"), payload...), addr); err != nil {
		t.Fatal(err)
	}
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err = client.ReadFrom(buf)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf[:n], append([]byte("reply:"), payload...)) {
		t.Fatal("client payload mismatch")
	}
}

func TestDNSTunnelWireCodec(t *testing.T) {
	payload := make([]byte, 96)
	_, _ = rand.Read(payload)
	payload[8] = 0xe3
	query, err := buildTunnelQuery(payload, "ns.vshell.test")
	if err != nil {
		t.Fatal(err)
	}
	id, question, _, got, ok := parseTunnelQuery(query, "ns.vshell.test")
	if !ok || !bytes.Equal(got, payload[12:]) {
		t.Fatalf("query codec mismatch: ok=%v got=%d", ok, len(got))
	}
	responsePayload := payload[:70]
	response := buildTXTResponse(id, question, responsePayload)
	decoded, ok := parseTXTResponse(response)
	if !ok || !bytes.Equal(decoded, responsePayload) {
		t.Fatalf("response codec mismatch: ok=%v got=%d", ok, len(decoded))
	}
}

func TestDNSTunnelRoundTrip(t *testing.T) {
	listener, err := ListenDNSTunnel("127.0.0.1:0", "ns.vshell.test")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverErr := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverErr <- acceptErr
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		buf := make([]byte, 24*len("dns-tunnel-payload-"))
		n, readErr := io.ReadFull(conn, buf)
		if readErr == nil {
			_, readErr = conn.Write(append([]byte("echo:"), buf[:n]...))
		}
		serverErr <- readErr
	}()
	client, err := DialDNSTunnel("dns", listener.Addr().String(), "ns.vshell.test")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(10 * time.Second))
	want := bytes.Repeat([]byte("dns-tunnel-payload-"), 24)
	if _, err = client.Write(want); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(want)+5)
	if _, err = io.ReadFull(client, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, append([]byte("echo:"), want...)) {
		t.Fatalf("round trip mismatch: got %d bytes", len(got))
	}
	if err = <-serverErr; err != nil {
		t.Fatal(err)
	}
}
