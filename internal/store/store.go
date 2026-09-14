package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"vshell/internal/model"
)

type Store struct {
	mu   sync.RWMutex
	path string
	s    model.State
}

func Open(path string) (*Store, error) {
	st := &Store{path: path, s: model.State{NextClientID: 1, NextListenerID: 1, NextTunnelID: 1}}
	b, err := os.ReadFile(path)
	if err == nil && len(b) != 0 {
		if err := json.Unmarshal(b, &st.s); err != nil {
			return nil, err
		}
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for i := range st.s.Clients {
		st.s.Clients[i].IsConnect = false
	}
	for i := range st.s.Listeners {
		st.s.Listeners[i].RunStatus = false
	}
	for i := range st.s.Tunnels {
		st.s.Tunnels[i].IsConnect = false
		st.s.Tunnels[i].RunStatus = false
	}
	return st, st.saveLocked()
}

func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.s, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) Snapshot() model.State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, _ := json.Marshal(s.s)
	var out model.State
	_ = json.Unmarshal(b, &out)
	return out
}

func (s *Store) AddListener(v model.Listener) (model.Listener, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v.ID = s.s.NextListenerID
	s.s.NextListenerID++
	s.s.Listeners = append(s.s.Listeners, v)
	return v, s.saveLocked()
}
func (s *Store) UpdateListener(v model.Listener) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.s.Listeners {
		if s.s.Listeners[i].ID == v.ID {
			s.s.Listeners[i] = v
			return s.saveLocked()
		}
	}
	return errors.New("listener not found")
}
func (s *Store) DeleteListener(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.s.Listeners {
		if s.s.Listeners[i].ID == id {
			s.s.Listeners = append(s.s.Listeners[:i], s.s.Listeners[i+1:]...)
			return s.saveLocked()
		}
	}
	return errors.New("listener not found")
}
func (s *Store) Listener(id int64) (model.Listener, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, v := range s.s.Listeners {
		if v.ID == id {
			return v, true
		}
	}
	return model.Listener{}, false
}
func (s *Store) Listeners() []model.Listener {
	out := s.Snapshot().Listeners
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *Store) UpsertClient(v model.Client) (model.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.s.Clients {
		if s.s.Clients[i].VerifyKey == v.VerifyKey && v.VerifyKey != "" {
			v.ID = s.s.Clients[i].ID
			v.Remark = s.s.Clients[i].Remark
			s.s.Clients[i] = v
			return v, s.saveLocked()
		}
	}
	v.ID = s.s.NextClientID
	s.s.NextClientID++
	s.s.Clients = append(s.s.Clients, v)
	return v, s.saveLocked()
}
func (s *Store) UpdateClient(v model.Client) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.s.Clients {
		if s.s.Clients[i].ID == v.ID {
			s.s.Clients[i] = v
			return s.saveLocked()
		}
	}
	return errors.New("client not found")
}
func (s *Store) Client(id int64) (model.Client, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, v := range s.s.Clients {
		if v.ID == id {
			return v, true
		}
	}
	return model.Client{}, false
}
func (s *Store) Clients() []model.Client { return s.Snapshot().Clients }
func (s *Store) DeleteClients(ids map[int64]bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.s.Clients[:0]
	for _, v := range s.s.Clients {
		if !ids[v.ID] {
			out = append(out, v)
		}
	}
	s.s.Clients = out
	return s.saveLocked()
}

func (s *Store) AddTunnel(v model.Tunnel) (model.Tunnel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v.ID = s.s.NextTunnelID
	s.s.NextTunnelID++
	s.s.Tunnels = append(s.s.Tunnels, v)
	return v, s.saveLocked()
}
func (s *Store) UpdateTunnel(v model.Tunnel) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.s.Tunnels {
		if s.s.Tunnels[i].ID == v.ID {
			s.s.Tunnels[i] = v
			return s.saveLocked()
		}
	}
	return errors.New("tunnel not found")
}
func (s *Store) DeleteTunnel(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.s.Tunnels {
		if s.s.Tunnels[i].ID == id {
			s.s.Tunnels = append(s.s.Tunnels[:i], s.s.Tunnels[i+1:]...)
			return s.saveLocked()
		}
	}
	return errors.New("tunnel not found")
}
func (s *Store) Tunnel(id int64) (model.Tunnel, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, v := range s.s.Tunnels {
		if v.ID == id {
			return v, true
		}
	}
	return model.Tunnel{}, false
}
func (s *Store) Tunnels() []model.Tunnel { return s.Snapshot().Tunnels }

func (s *Store) Settings() model.Settings { s.mu.RLock(); defer s.mu.RUnlock(); return s.s.Settings }
func (s *Store) SetSettings(v model.Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.s.Settings = v
	return s.saveLocked()
}

func FilterClients(items []model.Client, search string) []model.Client {
	if search == "" {
		return items
	}
	search = strings.ToLower(search)
	out := make([]model.Client, 0, len(items))
	for _, v := range items {
		if strings.Contains(strings.ToLower(v.Remark+" "+v.Addr+" "+v.HostName+" "+v.UserName+" "+v.OsName), search) {
			out = append(out, v)
		}
	}
	return out
}
