package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type Server struct {
	cfg          *Config
	pdd          *PddClient
	tokens       *TokenStore
	states       *StateStore
	callbackPath string
}

func NewServer(cfg *Config, pdd *PddClient, tokens *TokenStore) (*Server, error) {
	u, err := url.Parse(cfg.PDD.RedirectURI)
	if err != nil || u.Path == "" {
		return nil, fmt.Errorf("redirect_uri 无效: %q", cfg.PDD.RedirectURI)
	}
	return &Server{
		cfg:          cfg,
		pdd:          pdd,
		tokens:       tokens,
		states:       NewStateStore(),
		callbackPath: u.Path,
	}, nil
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/authorize", s.handleAuthorize)
	mux.HandleFunc(s.callbackPath, s.handleCallback)
	mux.HandleFunc("/success", s.handleSuccess)
	mux.HandleFunc("/orders", s.handleOrders)
	mux.HandleFunc("/tokens", s.handleTokens)
	mux.HandleFunc("/healthz", s.handleHealth)
	return mux
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html>
<html lang="zh-CN">
<head><meta charset="utf-8"><title>拼多多回调测试</title></head>
<body>
<h1>拼多多开放平台 · 回调测试</h1>
<p>回调地址：<code>%s</code></p>
<p><a href="/authorize">点击开始商家授权</a></p>
<p><a href="/tokens">查看已授权商家</a></p>
</body>
</html>`, s.cfg.PDD.RedirectURI)
}

func (s *Server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	state, err := randomState()
	if err != nil {
		http.Error(w, "生成 state 失败", http.StatusInternalServerError)
		return
	}
	s.states.Put(state, 10*time.Minute)

	authURL := fmt.Sprintf("%s?response_type=code&client_id=%s&redirect_uri=%s&state=%s",
		s.cfg.PDD.AuthURL,
		s.cfg.PDD.ClientID,
		url.QueryEscape(s.cfg.PDD.RedirectURI),
		url.QueryEscape(state),
	)
	http.Redirect(w, r, authURL, http.StatusFound)
}

func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	code := q.Get("code")
	state := q.Get("state")

	if state == "" || !s.states.Consume(state) {
		http.Error(w, "非法请求：state 校验失败", http.StatusBadRequest)
		return
	}
	if code == "" {
		http.Error(w, "缺少授权码 code", http.StatusBadRequest)
		return
	}

	token, err := s.pdd.ExchangeToken(code)
	if err != nil {
		log.Printf("换取 token 失败: %v", err)
		http.Error(w, "换取 token 失败："+err.Error(), http.StatusInternalServerError)
		return
	}

	rec := TokenRecord{
		OwnerID:      token.OwnerID,
		OwnerName:    token.OwnerName,
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		Scope:        token.Scope,
		ExpiresIn:    token.ExpiresIn,
		FetchedAt:    time.Now().Unix(),
	}
	if err := s.tokens.Save(rec); err != nil {
		log.Printf("存储 token 失败: %v", err)
		http.Error(w, "存储 token 失败", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/success?owner_id="+url.QueryEscape(rec.OwnerID)+"&owner_name="+url.QueryEscape(rec.OwnerName), http.StatusFound)
}

func (s *Server) handleSuccess(w http.ResponseWriter, r *http.Request) {
	ownerID := r.URL.Query().Get("owner_id")
	ownerName := r.URL.Query().Get("owner_name")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html>
<html lang="zh-CN">
<head><meta charset="utf-8"><title>授权成功</title></head>
<body>
<h1>授权成功</h1>
<p>商家：%s</p>
<p>owner_id：<code>%s</code></p>
<p><a href="/orders?owner_id=%s">查询该商家订单</a></p>
</body>
</html>`, ownerName, ownerID, url.QueryEscape(ownerID))
}

func (s *Server) handleOrders(w http.ResponseWriter, r *http.Request) {
	ownerID := r.URL.Query().Get("owner_id")
	if ownerID == "" {
		http.Error(w, "缺少 owner_id 参数", http.StatusBadRequest)
		return
	}
	rec, ok := s.tokens.Get(ownerID)
	if !ok {
		http.Error(w, "未找到该商家的授权 token", http.StatusNotFound)
		return
	}

	// 提前 5 分钟过期就刷新
	if rec.ExpiresAt() <= time.Now().Unix()+300 {
		newTok, err := s.pdd.RefreshToken(rec.RefreshToken)
		if err != nil {
			log.Printf("刷新 token 失败: %v", err)
			http.Error(w, "刷新 token 失败："+err.Error(), http.StatusInternalServerError)
			return
		}
		rec.AccessToken = newTok.AccessToken
		rec.RefreshToken = newTok.RefreshToken
		rec.ExpiresIn = newTok.ExpiresIn
		rec.FetchedAt = time.Now().Unix()
		if err := s.tokens.Save(rec); err != nil {
			http.Error(w, "保存刷新后的 token 失败", http.StatusInternalServerError)
			return
		}
	}

	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page <= 0 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	if pageSize <= 0 {
		pageSize = 20
	}

	orders, total, err := s.pdd.OrderList(rec.AccessToken, page, pageSize)
	if err != nil {
		http.Error(w, "查询订单失败："+err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"owner_id": ownerID,
		"total":    total,
		"page":     page,
		"orders":   orders,
	})
}

func (s *Server) handleTokens(w http.ResponseWriter, r *http.Request) {
	all := s.tokens.All()
	out := make([]map[string]any, 0, len(all))
	for _, rec := range all {
		out = append(out, map[string]any{
			"owner_id":            rec.OwnerID,
			"owner_name":          rec.OwnerName,
			"access_token_masked": maskToken(rec.AccessToken),
			"expires_at":          rec.ExpiresAt(),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": len(out), "tokens": out})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func randomState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func maskToken(s string) string {
	if len(s) <= 8 {
		return "****"
	}
	return s[:4] + "****" + s[len(s)-4:]
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
