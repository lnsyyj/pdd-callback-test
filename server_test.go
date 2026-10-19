// 本文件是 HTTP 接口层的测试，使用标准库 net/http/httptest 在进程内模拟请求，
// 不启动真实端口、不访问外网，因此可以离线、快速地验证路由与参数校验逻辑。
//
// 覆盖范围：
//   - 健康检查返回 200；
//   - /authorize 正确 302 并带上必需参数；
//   - 回调接口会拒绝非法 state。
//
// 未覆盖：真实换取 token、查询订单——这些依赖拼多多网关，属于集成测试范畴。
package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// newTestServer 构造一个用于测试的 Server 实例，避免每个测试重复搭建。
//
// 参数：
//   - t：测试上下文，用于在失败时终止用例并输出原因。
//
// 返回值：
//   - *Server：可直接调用 routes() 的服务实例；
//   - *TokenStore：与 Server 绑定的存储，便于用例中途改写数据。
//
// 关键设计：
//   - token 存储路径使用 t.TempDir()，每个用例拿到独立的临时目录，
//     用例结束由测试框架自动清理，用例之间不会互相污染；
//   - 配置里的 client_id / redirect_uri 均为假值，仅用于验证参数拼装，
//     不会真正访问外网。
//
// 副作用：在临时目录下创建 token 存储（可能创建目录），不发起网络请求。
func newTestServer(t *testing.T) (*Server, *TokenStore) {
	// t.Helper 让失败信息指向调用方所在行，而不是指向本函数内部。
	t.Helper()

	// 构造测试配置。注意 redirect_uri 的路径会被 NewServer 用作回调路由，
	// 所以下面测试回调时使用的路径必须与之一致。
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

	// 使用临时目录存放 token，保证用例隔离。
	tokens, err := NewTokenStore(t.TempDir() + "/tokens.json")
	if err != nil {
		t.Fatal(err)
	}

	// 组装客户端与服务。
	pdd := NewPddClient(cfg.PDD.ClientID, cfg.PDD.ClientSecret, cfg.PDD.Gateway)
	srv, err := NewServer(cfg, pdd, tokens)
	if err != nil {
		t.Fatal(err)
	}
	return srv, tokens
}

// TestHealthz 验证健康检查接口返回 200 且响应体含 "ok"。
//
// 目的：确认路由已正确注册、JSON 序列化可用。这是最小的一条冒烟测试，
// 若它失败，通常意味着路由表或 writeJSON 出了问题。
func TestHealthz(t *testing.T) {
	srv, _ := newTestServer(t)

	// 构造一个 GET /healthz 请求，并用 ResponseRecorder 捕获响应。
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", rec.Code)
	}
	// 只断言包含 "ok"，不比对完整 JSON，避免字段顺序或空格变化导致测试脆弱。
	if !strings.Contains(rec.Body.String(), `"ok"`) {
		t.Fatalf("响应体异常: %s", rec.Body.String())
	}
}

// TestAuthorizeRedirect 验证 /authorize 会 302 跳转，且跳转地址中带有
// client_id 与 state 两个必需参数。
//
// 目的：授权链接拼错会导致商家无法进入授权页，而这类错误在浏览器里表现为
// 「跳到拼多多后报参数错误」，很难定位，所以用单测锁住。
//
// 注意：state 只断言「非空」，因为它每次随机生成，无法比对具体值。
func TestAuthorizeRedirect(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/authorize", nil)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)

	// 必须是 302，说明走的是跳转而不是直接渲染页面。
	if rec.Code != http.StatusFound {
		t.Fatalf("期望 302，实际 %d", rec.Code)
	}

	// 解析 Location 头，检查查询参数。
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

// TestCallbackRejectsBadState 验证回调接口会拒绝未签发的 state。
//
// 目的：state 校验是防 CSRF 的关键一环，若被绕过，攻击者可诱导他人把账号
// 绑定到攻击者的店铺。这里用随便编造的 state 模拟伪造回调，期望返回 400。
//
// 说明：本用例只覆盖「state 不存在」这一种情形；
// 「state 存在但已过期」同样返回 400，逻辑在 StateStore.Consume 中。
func TestCallbackRejectsBadState(t *testing.T) {
	srv, _ := newTestServer(t)

	// 注意路径要与配置中的 redirect_uri 路径一致，否则会落到首页兜底路由上。
	req := httptest.NewRequest(http.MethodGet, "/pdd/oauth/callback?code=abc&state=bad", nil)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("期望 400，实际 %d", rec.Code)
	}
}
