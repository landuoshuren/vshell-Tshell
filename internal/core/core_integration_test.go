package core_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"vshell/internal/agent"
	"vshell/internal/core"
	"vshell/internal/model"
	"vshell/internal/store"
)

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

func freeUDPAddr(t *testing.T) string {
	t.Helper()
	addr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	c, err := net.ListenUDP("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	value := c.LocalAddr().String()
	_ = c.Close()
	return value
}

func waitFor(t *testing.T, limit time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition was not met before timeout")
}

func TestAgentRPCFileAndTunnel(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	c := core.New(st)
	defer c.Close()
	listenerAddr := freeAddr(t)
	l, err := st.AddListener(model.Listener{Mode: "tcp", ListenAddr: listenerAddr, ConnectAddr: listenerAddr, Vkey: "integration-vkey", PingInterval: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.StartListener(l.ID); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	agentDone := make(chan struct{})
	defer func() {
		cancel()
		select {
		case <-agentDone:
		case <-time.After(2 * time.Second):
			t.Error("agent did not stop before test cleanup")
		}
	}()
	go func() {
		defer close(agentDone)
		_ = agent.New(agent.Config{ServerAddr: listenerAddr, Vkey: "integration-vkey", VerifyKey: "integration-client"}).Run(ctx)
	}()
	waitFor(t, 5*time.Second, func() bool { return c.IsOnline(1) })

	command := "echo VSHELL_CORE_TEST"
	if runtime.GOOS == "windows" {
		command = "cmd /c echo VSHELL_CORE_TEST"
	}
	raw, err := c.Call(context.Background(), 1, "shell", map[string]any{"command": command})
	if err != nil {
		t.Fatal(err)
	}
	var shell string
	if err := json.Unmarshal(raw, &shell); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(shell, "VSHELL_CORE_TEST") {
		t.Fatalf("unexpected shell output %q", shell)
	}

	dir := t.TempDir()
	_, err = c.Call(context.Background(), 1, "file_edit", map[string]any{"path": dir, "target": "probe.txt", "content": "file-probe"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = c.Call(context.Background(), 1, "file_cat", map[string]any{"path": dir, "target": "probe.txt"})
	if err != nil {
		t.Fatal(err)
	}
	var cat struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &cat); err != nil {
		t.Fatal(err)
	}
	if cat.Content != "file-probe" {
		t.Fatalf("unexpected file content %q", cat.Content)
	}

	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			conn, e := echo.Accept()
			if e != nil {
				return
			}
			go func(v net.Conn) { defer v.Close(); _, _ = io.Copy(v, v) }(conn)
		}
	}()
	tunnelAddr := freeAddr(t)
	_, portText, _ := net.SplitHostPort(tunnelAddr)
	var port int
	_, _ = fmt.Sscan(portText, &port)
	tunnel, err := st.AddTunnel(model.Tunnel{ClientID: 1, Mode: "tcp", Port: port, Target: echo.Addr().String(), Status: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.StartTunnel(tunnel.ID); err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("tcp", tunnelAddr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte("tunnel-probe")); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 12)
	if _, err := io.ReadFull(conn, b); err != nil {
		t.Fatal(err)
	}
	if string(b) != "tunnel-probe" {
		t.Fatalf("unexpected tunnel response %q", b)
	}

	udpEcho, err := net.ListenUDP("udp", mustUDPAddr(t, "127.0.0.1:0"))
	if err != nil {
		t.Fatal(err)
	}
	defer udpEcho.Close()
	go func() {
		buf := make([]byte, 1024)
		for {
			n, peer, e := udpEcho.ReadFromUDP(buf)
			if e != nil {
				return
			}
			_, _ = udpEcho.WriteToUDP(buf[:n], peer)
		}
	}()
	udpTunnelAddr := freeUDPAddr(t)
	_, udpPortText, _ := net.SplitHostPort(udpTunnelAddr)
	var udpPort int
	_, _ = fmt.Sscan(udpPortText, &udpPort)
	udpTunnel, err := st.AddTunnel(model.Tunnel{ClientID: 1, Mode: "udp", Port: udpPort, Target: udpEcho.LocalAddr().String(), Status: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.StartTunnel(udpTunnel.ID); err != nil {
		t.Fatal(err)
	}
	udpConn, err := net.DialUDP("udp", nil, mustUDPAddr(t, udpTunnelAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer udpConn.Close()
	_ = udpConn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := udpConn.Write([]byte("udp-probe")); err != nil {
		t.Fatal(err)
	}
	udpResponse := make([]byte, 32)
	n, err := udpConn.Read(udpResponse)
	if err != nil {
		t.Fatal(err)
	}
	if string(udpResponse[:n]) != "udp-probe" {
		t.Fatalf("unexpected udp tunnel response %q", udpResponse[:n])
	}
}

func mustUDPAddr(t *testing.T, value string) *net.UDPAddr {
	t.Helper()
	addr, err := net.ResolveUDPAddr("udp", value)
	if err != nil {
		t.Fatal(err)
	}
	return addr
}
