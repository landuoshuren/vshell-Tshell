package protocol

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type ossTestStore struct {
	mu      sync.RWMutex
	objects map[string][]byte
}

func (s *ossTestStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/bucket" || r.URL.Path == "/bucket/" {
		w.WriteHeader(http.StatusOK)
		return
	}
	switch r.Method {
	case http.MethodPut:
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.objects[r.URL.Path] = body
		s.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		s.mu.RLock()
		body, ok := s.objects[r.URL.Path]
		s.mu.RUnlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	case http.MethodHead:
		s.mu.RLock()
		_, ok := s.objects[r.URL.Path]
		s.mu.RUnlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	case http.MethodDelete:
		s.mu.Lock()
		delete(s.objects, r.URL.Path)
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func TestOSSTunnelRoundTripAndReconnect(t *testing.T) {
	fixture := httptest.NewServer(&ossTestStore{objects: make(map[string][]byte)})
	defer fixture.Close()
	baseURL := fixture.URL + "/bucket"
	key := "0123456789abcdef0123456789abcdef"
	listener, err := ListenOSSTunnel(baseURL, key)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	for iteration := 0; iteration < 2; iteration++ {
		accepted := make(chan interface{}, 1)
		go func() {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				accepted <- acceptErr
				return
			}
			accepted <- conn
		}()
		client, dialErr := DialOSSTunnel(baseURL, key)
		if dialErr != nil {
			t.Fatal(dialErr)
		}
		value := <-accepted
		server, ok := value.(interface {
			Read([]byte) (int, error)
			Write([]byte) (int, error)
			Close() error
		})
		if !ok {
			t.Fatalf("accept failed: %v", value)
		}
		_ = client.SetDeadline(time.Now().Add(5 * time.Second))
		payload := []byte("client-to-server")
		if _, err = client.Write(payload); err != nil {
			t.Fatal(err)
		}
		buffer := make([]byte, len(payload))
		if _, err = io.ReadFull(server, buffer); err != nil || string(buffer) != string(payload) {
			t.Fatalf("server read: %q, %v", buffer, err)
		}
		response := []byte("server-to-client")
		if _, err = server.Write(response); err != nil {
			t.Fatal(err)
		}
		buffer = make([]byte, len(response))
		if _, err = io.ReadFull(client, buffer); err != nil || string(buffer) != string(response) {
			t.Fatalf("client read: %q, %v", buffer, err)
		}
		_ = client.Close()
		_ = server.Close()
	}
}
