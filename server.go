// 本文件是「HTTP 接口层」，负责把外部请求翻译成对存储层（store.go）与
// 拼多多客户端（pdd.go）的调用，并把结果写回浏览器。
//
// 接口清单（与 README / 使用文档保持一致，改动请同步更新文档）：
//
//	GET /                      首页，含授权入口
//	GET /authorize             生成 state 并 302 跳转到拼多多授权页
//	GET {redirect_uri 的路径}   授权回调：校验 state → code 换 token → 落库 → 302 /success
//	GET /success               授权成功页
//	GET /orders                查询订单（必要时先自动刷新 token）
//	GET /tokens                查看已授权商家（token 打码）
//	GET /healthz               健康检查
//
// 分层约定：handler 只做「取参数 → 调下层 → 写响应」，
// 不直接发 HTTP 请求、不直接读写文件，这些都交给 pdd.go / store.go。
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

// Server 是 HTTP 服务的运行时上下文，聚合了处理请求所需的全部分依赖。
//
// 它在启动时被构造一次，之后只读，可被所有请求 goroutine 并发共享。
type Server struct {
	// cfg 是全局配置。handler 主要从中读取 redirect_uri、client_id、auth_url。
	cfg *Config

	// pdd 是拼多多 API 客户端，用于换 token、刷 token、查订单。
	pdd *PddClient

	// tokens 是商家授权凭证的存储（内存 + JSON 文件）。
	tokens *TokenStore

	// states 是 OAuth state 的内存存储，一次性消费，用于防 CSRF 与防重放。
	states *StateStore

	// callbackPath 是回调接口的路由路径，取自 redirect_uri 的路径部分。
	// 例：redirect_uri 为 https://erp.example.com/auth/pdd/callback 时为 "/auth/pdd/callback"。
	// 之所以动态确定，是为了让回调地址与拼多多控制台登记的值天然一致，无需额外配置。
	callbackPath string
}

// NewServer 构造 HTTP 服务对象，并校验回调地址是否可用。
//
// 参数：
//   - cfg：全局配置，必须非 nil，且 cfg.PDD.RedirectURI 必须是带路径的合法 URL；
//   - pdd：已初始化的拼多多 API 客户端；
//   - tokens：已初始化的 token 存储。
//
// 返回值：
//   - *Server：可直接用于注册路由的服务对象；
//   - error：redirect_uri 无法解析、或解析后路径为空（例如填成 "https://a.com"）时返回错误。
//
// 副作用：解析 URL，并创建一个空的 StateStore。不监听端口，也不发起网络请求。
//
// 为什么要求路径非空：回调接口的路由就来自这个路径，为空会导致接口挂到 "/" 上，
// 把首页覆盖掉，属于难以排查的配置错误，所以在这里就拦下来。
func NewServer(cfg *Config, pdd *PddClient, tokens *TokenStore) (*Server, error) {
	u, err := url.Parse(cfg.PDD.RedirectURI)
	// u.Path 为空说明配置的是一个没有路径的地址，无法确定回调路由。
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

// routes 注册全部路由并返回一个可直接交给 http.ListenAndServe 的 Handler。
//
// 返回值：
//   - http.Handler：内部是一个 http.ServeMux，已挂载 7 个接口。
//
// 注意 Go 标准库 ServeMux 的匹配规则：
//   - "/" 是「兜底模式」，会匹配所有未被更具体路径命中的请求，
//     因此 handleIndex 内部还要自行判断路径是否为 "/"，否则任意错误路径都会返回首页。
//   - 其余带具体路径的注册项优先于 "/"，所以顺序不影响匹配结果。
//
// 每次调用都会新建一个 ServeMux。本程序只在 main 中调用一次，
// 测试中会重复调用以获得独立实例。
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)               // 首页（兜底）
	mux.HandleFunc("/authorize", s.handleAuthorize)  // 发起授权
	mux.HandleFunc(s.callbackPath, s.handleCallback) // 授权回调（路径来自配置）
	mux.HandleFunc("/success", s.handleSuccess)      // 授权成功页
	mux.HandleFunc("/orders", s.handleOrders)        // 查询订单
	mux.HandleFunc("/tokens", s.handleTokens)        // 查看已授权商家
	mux.HandleFunc("/healthz", s.handleHealth)       // 健康检查
	return mux
}

// handleIndex 渲染首页，提供「开始授权」与「查看已授权商家」两个入口。
//
// 参数：
//   - w：响应写入器；
//   - r：请求对象，仅使用其 URL.Path。
//
// 行为：
//   - 路径不是 "/" 时返回 404。因为 "/" 在 ServeMux 中是兜底模式，
//     不加这层判断的话，访问任何不存在的地址都会拿到首页，容易误导排查。
//   - 否则输出一段最小 HTML，其中展示当前配置的回调地址，方便与控制台核对。
//
// 响应：Content-Type 为 text/html; charset=utf-8。
//
// 注意：回调地址是直接插入 HTML 的，未做转义。该值来自本地配置文件而非用户输入，
// 风险可控；若将来允许外部修改配置，应补上 HTML 转义。
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

// handleAuthorize 发起商家授权：生成一次性 state，然后 302 跳转到拼多多授权页。
//
// 参数：
//   - w：响应写入器，用于写 302 与 Location 头；
//   - r：请求对象。本接口不使用任何查询参数，浏览器直接访问即可。
//
// 流程：
//  1. 生成 16 字节随机 state（十六进制 32 字符）；
//  2. 把 state 登记到内存，有效期 10 分钟；
//  3. 拼接拼多多授权页 URL，并把浏览器 302 过去。
//
// 授权页 URL 的四个参数：
//   - response_type=code：固定值，表示要走「授权码」模式；
//   - client_id：应用标识；
//   - redirect_uri：授权完成后回跳的地址，必须与控制台登记值完全一致，
//     因此这里做了 URL 编码（其中含 :// 等字符）；
//   - state：原样带回，用于回调时校验请求来源。
//
// 响应：
//   - 成功：302，Location 为拼多多授权页地址；
//   - 失败：500，生成随机数失败（极少见，通常意味着系统熵源异常）。
//
// 注意：不要用 curl 直接访问本接口「验证」，它只是跳转入口，
// 需要浏览器跟随跳转才能完成后续的登录与授权。
func (s *Server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	// 生成随机 state。失败说明 crypto/rand 不可用，属于系统级异常，直接报 500。
	state, err := randomState()
	if err != nil {
		http.Error(w, "生成 state 失败", http.StatusInternalServerError)
		return
	}
	// 登记 state，10 分钟内有效。超时后回调会因校验失败而返回 400，
	// 这是有意的：授权页停留过久应重新发起。
	s.states.Put(state, 10*time.Minute)

	// 拼接授权页地址。
	// redirect_uri 与 state 都需要 URL 编码，client_id 是十六进制字符串无需编码。
	authURL := fmt.Sprintf("%s?response_type=code&client_id=%s&redirect_uri=%s&state=%s",
		s.cfg.PDD.AuthURL,
		s.cfg.PDD.ClientID,
		url.QueryEscape(s.cfg.PDD.RedirectURI),
		url.QueryEscape(state),
	)
	http.Redirect(w, r, authURL, http.StatusFound)
}

// handleCallback 处理拼多多的授权回调，是整条 OAuth 链路的核心。
//
// 参数：
//   - w：响应写入器；
//   - r：请求对象，从 URL 查询参数中读取：
//   - code：拼多多下发的授权码，有效期 10 分钟；
//   - state：发起授权时生成的一次性随机串，用于防 CSRF。
//
// 流程（任一步失败即中断并返回错误）：
//  1. 校验 state：必须存在、未过期、且未被使用过（Consume 会立即删除）；
//  2. 校验 code 非空；
//  3. 用 code 调拼多多换取 access_token；
//  4. 组装 TokenRecord 并落库（以 owner_id 为主键，重复授权会覆盖）；
//  5. 302 跳到 /success 展示结果。
//
// 响应：
//   - 400：state 缺失/非法/已用过/已过期，或缺少 code；
//   - 500：换 token 失败（code 失效、签名错误、网络异常等）或落库失败；
//   - 302：成功，跳转 /success 并携带 owner_id、owner_name。
//
// 副作用：
//   - 消耗一个 state（不可重复使用）；
//   - 发起一次对拼多多的网络请求；
//   - 写入 token 存储文件。
//
// 安全说明：state 校验放在最前面，可以挡掉伪造的回调请求，
// 避免攻击者用自己的 code 把你的服务与他的店铺绑定。
func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	code := q.Get("code")
	state := q.Get("state")

	// 第 1 步：校验 state。Consume 是「一次性」语义，校验通过后该 state 立即失效，
	// 因此刷新回调页或用历史链接重放都会失败。
	if state == "" || !s.states.Consume(state) {
		http.Error(w, "非法请求：state 校验失败", http.StatusBadRequest)
		return
	}
	// 第 2 步：code 必须存在。用户在授权页点了「拒绝」时，回跳不带 code。
	if code == "" {
		http.Error(w, "缺少授权码 code", http.StatusBadRequest)
		return
	}

	// 第 3 步：用 code 换 token。这一步需要访问拼多多网关。
	token, err := s.pdd.ExchangeToken(code)
	if err != nil {
		// 常见原因：code 超过 10 分钟、code 已被使用、client_id/secret 错误、
		// redirect_uri 与控制台不一致导致签名校验不过。
		log.Printf("换取 token 失败: %v", err)
		http.Error(w, "换取 token 失败："+err.Error(), http.StatusInternalServerError)
		return
	}

	// 第 4 步：组装本地持久化记录。
	// 关键点：FetchedAt 记录「此刻」，它与 ExpiresIn 相加即为过期时刻，
	// 后续判断是否需要刷新全靠这两个字段，因此不能用接口返回的其他时间字段替代。
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
		// 落库失败时 token 已拿到但没存下，本次授权等于白做，只能让用户重试。
		log.Printf("存储 token 失败: %v", err)
		http.Error(w, "存储 token 失败", http.StatusInternalServerError)
		return
	}

	// 第 5 步：跳转到成功页。商家名可能含中文或空格，因此做 URL 编码。
	http.Redirect(w, r, "/success?owner_id="+url.QueryEscape(rec.OwnerID)+"&owner_name="+url.QueryEscape(rec.OwnerName), http.StatusFound)
}

// handleSuccess 渲染授权成功页，展示商家信息并提供「查询订单」的快捷入口。
//
// 参数：
//   - w：响应写入器；
//   - r：请求对象，从查询参数读取：
//   - owner_id：商家 ID，由回调 302 时携带；
//   - owner_name：商家名称，由回调 302 时携带。
//
// 响应：Content-Type 为 text/html; charset=utf-8。
//
// 说明：两个参数都只用于展示，缺失时页面显示为空，不会报错。
// 该页不做任何鉴权——它只展示刚刚授权成功的商家信息，
// 若用于生产，应加上会话校验，避免他人通过 owner_id 窥探商家名。
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

// handleOrders 查询指定商家的订单，演示「授权成功后调用业务接口」。
//
// 参数：
//   - w：响应写入器；
//   - r：请求对象，从查询参数读取：
//   - owner_id（必填）：商家 ID，用于定位本地保存的授权记录；
//   - page（选填）：页码，默认 1；非法值（非数字或 ≤0）也回落到 1；
//   - page_size（选填）：每页条数，默认 20；非法值同样回落到 20。
//
// 流程：
//  1. 取 owner_id 并校验；
//  2. 从本地存储取出该商家的授权记录，没有则 404；
//  3. 若 token 剩余有效期不足 5 分钟，先用 refresh_token 刷新并落库；
//  4. 用（可能是刚刷新的）access_token 调订单接口；
//  5. 以 JSON 返回订单列表与总数。
//
// 响应：
//   - 400：缺少 owner_id；
//   - 404：本地没有该商家的授权记录（未授权，或存储被清空）；
//   - 500：刷新 token 失败、保存刷新结果失败、或查询订单失败；
//   - 200：JSON，形如 {"owner_id":..,"total":..,"page":..,"orders":[..]}。
//     注意返回体里没有回显 page_size，只回显 page，属示例的简化。
//
// 副作用：
//   - 可能发起最多两次网络请求（刷新 + 查询）；
//   - 刷新成功时会改写 token 存储文件。
//
// 关于提前刷新：这里用「剩余不足 300 秒」作为阈值，而非等到真正过期，
// 是为了避免请求发出后 token 恰好在途中失效导致的偶发失败。
// 生产环境建议把阈值放宽到 30 分钟，并配合定时任务主动刷新。
func (s *Server) handleOrders(w http.ResponseWriter, r *http.Request) {
	// 1. 必填参数校验。
	ownerID := r.URL.Query().Get("owner_id")
	if ownerID == "" {
		http.Error(w, "缺少 owner_id 参数", http.StatusBadRequest)
		return
	}

	// 2. 取本地授权记录。取不到说明该商家从未授权，或数据文件被清理过，
	//    此时既不刷新也无法查询，直接 404 让调用方去重新授权。
	rec, ok := s.tokens.Get(ownerID)
	if !ok {
		http.Error(w, "未找到该商家的授权 token", http.StatusNotFound)
		return
	}

	// 3. 提前 5 分钟过期就刷新。
	//    ExpiresAt() = FetchedAt + ExpiresIn，与「当前时间 + 300」比较：
	//    剩余不足 5 分钟（或已过期）就刷新。
	if rec.ExpiresAt() <= time.Now().Unix()+300 {
		newTok, err := s.pdd.RefreshToken(rec.RefreshToken)
		if err != nil {
			// refresh_token 也失效了（例如超过授权时长），此时只能让商家重新授权。
			log.Printf("刷新 token 失败: %v", err)
			http.Error(w, "刷新 token 失败："+err.Error(), http.StatusInternalServerError)
			return
		}
		// 用新值覆盖旧值。特别注意 RefreshToken 必须一并更新：
		// 拼多多是滚动刷新，旧的 refresh_token 刷新后即失效，
		// 只更新 access_token 会导致下次刷新必然失败。
		rec.AccessToken = newTok.AccessToken
		rec.RefreshToken = newTok.RefreshToken
		rec.ExpiresIn = newTok.ExpiresIn
		rec.FetchedAt = time.Now().Unix() // 重置计时起点，否则下次判断仍会认为已过期
		if err := s.tokens.Save(rec); err != nil {
			// 刷新成功但没存下来：本次请求还能用新 token，
			// 但下次请求会拿着已失效的旧 refresh_token，因此必须报错暴露问题。
			http.Error(w, "保存刷新后的 token 失败", http.StatusInternalServerError)
			return
		}
	}

	// 4. 分页参数：容忍缺失与非法值，统一回落到默认值。
	//    Atoi 失败时返回 0，配合 <=0 的判断即可同时处理「非数字」和「≤0」两种情况。
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page <= 0 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	if pageSize <= 0 {
		pageSize = 20
	}

	// 5. 调拼多多订单接口。
	orders, total, err := s.pdd.OrderList(rec.AccessToken, page, pageSize)
	if err != nil {
		http.Error(w, "查询订单失败："+err.Error(), http.StatusInternalServerError)
		return
	}

	// 6. 统一 JSON 输出。
	writeJSON(w, http.StatusOK, map[string]any{
		"owner_id": ownerID,
		"total":    total,
		"page":     page,
		"orders":   orders,
	})
}

// handleTokens 列出所有已授权商家，供调试与运营查看授权状态。
//
// 参数：
//   - w：响应写入器；
//   - r：请求对象，不使用任何参数。
//
// 响应：200，JSON 形如 {"count":1,"tokens":[{"owner_id":..,"owner_name":..,
// "access_token_masked":..,"expires_at":..}]}。
//
// 安全设计：
//   - access_token 只返回打码后的形式（首 4 位 + **** + 末 4 位），
//     避免完整令牌通过接口外泄；
//   - 但 owner_id、owner_name、expires_at 仍属商家敏感信息，
//     生产环境应加鉴权后再暴露本接口。
//
// 说明：expires_at 是 Unix 秒级时间戳，需自行换算成可读时间。
func (s *Server) handleTokens(w http.ResponseWriter, r *http.Request) {
	all := s.tokens.All()
	// 预分配容量，避免逐个 append 触发扩容。
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

// handleHealth 是健康检查接口，供负载均衡或监控探活使用。
//
// 参数：
//   - w：响应写入器；
//   - r：请求对象，不使用任何参数。
//
// 响应：200 + {"status":"ok"}。
//
// 说明：该接口只证明 HTTP 服务进程还活着，不校验下游依赖（拼多多网关、存储文件）
// 是否可用。若需要真实的就绪检查，应另加一个接口去探测这些依赖。
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// randomState 生成一个用于 OAuth 的随机 state 字符串。
//
// 返回值：
//   - string：32 个字符的十六进制串（16 字节随机数的 hex 编码）；
//   - error：crypto/rand 读取失败时返回错误（极少见）。
//
// 为什么用 crypto/rand 而不是 math/rand：
//
//	state 用于防 CSRF，必须不可预测。math/rand 的序列可被推导，不能用于安全场景。
//
// 16 字节（128 位）足以避免碰撞，也远超实际需要的安全强度。
func randomState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// maskToken 把令牌打码后再展示，降低泄露风险。
//
// 参数：
//   - s：原始令牌字符串。
//
// 返回值：
//   - 长度大于 8 时返回「前 4 位 + **** + 后 4 位」，便于人工核对是哪一个 token；
//   - 长度不超过 8（含空串）时统一返回 "****"，因为此时首尾拼接会暴露大部分内容。
//
// 注意：打码只防「肉眼泄露」与「误贴到聊天记录」，
// 若有权限读取完整值仍应通过加密存储与访问控制来保护。
func maskToken(s string) string {
	if len(s) <= 8 {
		return "****"
	}
	return s[:4] + "****" + s[len(s)-4:]
}

// writeJSON 是统一的 JSON 响应写法：设置响应头、状态码，再编码响应体。
//
// 参数：
//   - w：响应写入器；
//   - status：HTTP 状态码，如 200、400、500；
//   - v：任意可 JSON 序列化的值，通常是 map[string]any 或结构体。
//
// 副作用：一旦调用 WriteHeader 写出状态码，响应头即被锁定，
// 因此调用本函数后不能再修改 header 或状态码。
//
// 关于忽略 Encode 的错误：此时响应头与状态码已发出，无法再改成 500 告知客户端，
// 只能接受这次响应体可能为空的事实。生产环境可在此处补日志记录。
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
