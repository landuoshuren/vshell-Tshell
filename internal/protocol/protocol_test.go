package protocol

import (
	"net"
	"testing"
)

func TestMessageRoundTrip(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	want := Message{Type: "request", RequestID: "42", Action: "shell", Data: MarshalData(map[string]string{"command": "echo probe"})}
	errCh := make(chan error, 1)
	go func() { errCh <- NewConn(a).WriteMessage(want) }()
	got, err := NewConn(b).ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	if got.Type != want.Type || got.RequestID != want.RequestID || got.Action != want.Action || string(got.Data) != string(want.Data) {
		t.Fatalf("round trip mismatch: %#v", got)
	}
}
