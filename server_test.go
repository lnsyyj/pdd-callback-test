package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func newTestServer(t *testing.T) (*Server, *TokenStore) {
	t.Helper()
	cfg := &Config{
		Server: ServerConfig{Host: "127.0.0.1", Port: 8080},
		PDD: PDDConfig{
			ClientID:     "test_id",
			ClientSecret: "test_secret",
			RedirectURI:  "http://localhost:8080/pdd/oauth/callback",
			Gateway:      "https://gw-api.pinduoduo.com/api/router",
			AuthURL:      "https://mms.pinduoduo.com/open.html",
		},
		TokenStoreFile: "data/tokens.json",
	}
	tokens, err := NewTokenStore(t.TempDir() + "/tokens.json")
	if err != nil {
		t.Fatal(err)
	}
	pdd := NewPddClient(cfg.PDD.ClientID, cfg.PDD.ClientSecret, cfg.PDD.Gateway)
	srv, err := NewServer(cfg, pdd, tokens)
	if err != nil {
		t.Fatal(err)
	}
	return srv, tokens
}

func TestHealthz(t *testing.T) {
	srv, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"ok"`) {
		t.Fatalf("响应体异常: %s", rec.Body.String())
	}
}

func TestAuthorizeRedirect(t *testing.T) {
	srv, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/authorize", nil)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("期望 302，实际 %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Get("client_id") != "test_id" {
		t.Fatalf("授权链接缺少 client_id: %s", loc)
	}
	if u.Query().Get("state") == "" {
		t.Fatalf("授权链接缺少 state: %s", loc)
	}
}

func TestCallbackRejectsBadState(t *testing.T) {
	srv, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/pdd/oauth/callback?code=abc&state=bad", nil)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("期望 400，实际 %d", rec.Code)
	}
}
