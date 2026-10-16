package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type TokenRecord struct {
	OwnerID      string   `json:"owner_id"`
	OwnerName    string   `json:"owner_name"`
	AccessToken  string   `json:"access_token"`
	RefreshToken string   `json:"refresh_token"`
	Scope        []string `json:"scope"`
	ExpiresIn    int64    `json:"expires_in"`
	FetchedAt    int64    `json:"fetched_at"`
}

func (t *TokenRecord) ExpiresAt() int64 {
	return t.FetchedAt + t.ExpiresIn
}

type TokenStore struct {
	mu   sync.Mutex
	path string
	data map[string]TokenRecord
}

func NewTokenStore(path string) (*TokenStore, error) {
	ts := &TokenStore{path: path, data: map[string]TokenRecord{}}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ts, nil
		}
		return nil, err
	}
	if len(b) == 0 {
		return ts, nil
	}
	if err := json.Unmarshal(b, &ts.data); err != nil {
		return nil, err
	}
	return ts, nil
}

func (ts *TokenStore) Save(rec TokenRecord) error {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.data[rec.OwnerID] = rec
	return ts.persistLocked()
}

func (ts *TokenStore) Get(ownerID string) (TokenRecord, bool) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	r, ok := ts.data[ownerID]
	return r, ok
}

func (ts *TokenStore) All() []TokenRecord {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	out := make([]TokenRecord, 0, len(ts.data))
	for _, v := range ts.data {
		out = append(out, v)
	}
	return out
}

func (ts *TokenStore) persistLocked() error {
	b, err := json.MarshalIndent(ts.data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(ts.path, b, 0o600)
}

type StateStore struct {
	mu sync.Mutex
	m  map[string]int64
}

func NewStateStore() *StateStore {
	return &StateStore{m: map[string]int64{}}
}

func (s *StateStore) Put(state string, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[state] = time.Now().Add(ttl).Unix()
}

func (s *StateStore) Consume(state string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.m[state]
	if !ok {
		return false
	}
	delete(s.m, state)
	return time.Now().Unix() <= exp
}
