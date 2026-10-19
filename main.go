// Package main 实现拼多多开放平台「企业ERP」回调地址的最小可运行示例。
//
// 程序只依赖 Go 标准库，目的是用尽可能少的代码验证下面这条闭环是否被打通：
//
//	商家点「授权」 → 跳转拼多多授权页 → 回调地址收到 code
//	              → code 换取 access_token → 落库 → 调用业务接口（查订单）
//
// 运行方式：
//
//	go run . -config config.json
//
// 启动后可访问的页面与接口，见 server.go 中 routes() 的注册清单。
// 各文件职责：main.go 装配启动、config.go 配置、server.go HTTP 接口、
// pdd.go 拼多多 API 客户端、store.go 本地存储。
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
)

// main 是程序入口，负责把各组件「装配」起来并启动 HTTP 服务。
//
// 它本身不承载业务逻辑，职责只有四件事：
//  1. 解析命令行参数，加载并校验配置；
//  2. 构造三个核心依赖：TokenStore（token 存储）、PddClient（拼多多客户端）、Server（HTTP 服务）；
//  3. 注册路由；
//  4. 阻塞监听端口，等待商家授权与后续业务请求。
//
// 命令行参数：
//
//	-config string
//	    配置文件路径，默认 "config.json"（相对于进程启动时的工作目录）。
//
// 返回值：无。下列情况会调用 log.Fatalf 以退出码 1 终止进程：
//   - 配置文件读取失败或 JSON 格式非法；
//   - redirect_uri 为空（程序无法确定回调路由，继续启动没有意义）；
//   - token 存储初始化失败（例如目标目录不可写）；
//   - NewServer 构造失败（redirect_uri 不是合法 URL）；
//   - HTTP 端口监听失败（例如端口被占用）。
//
// 一切正常时，函数会阻塞在 http.ListenAndServe 中，直到进程被外部终止。
func main() {
	// 声明并解析命令行参数。
	// flag.String 返回的是 *string，所以下面用 *configPath 取实际值。
	// 不传 -config 时，默认读取当前工作目录下的 config.json。
	configPath := flag.String("config", "config.json", "配置文件路径")
	flag.Parse()

	// 加载配置。
	// loadConfig 内部会为缺省字段填充默认值（监听地址、端口、网关地址、
	// 授权页地址、token 落库路径等），因此正常情况下拿到的 cfg 各字段都已可用。
	cfg, err := loadConfig(*configPath)
	if err != nil {
		// 配置是一切的前提，读不到就直接退出，不做降级。
		log.Fatalf("读取配置失败: %v", err)
	}

	// 凭据校验：只告警、不退出。
	// 原因：本地调试阶段允许先跑通「启动 → 首页 → 授权跳转」这些不依赖凭据的环节，
	// 真正调拼多多接口时才会因凭据为空而失败，那时的错误信息更具体、更好定位。
	if cfg.PDD.ClientID == "" || cfg.PDD.ClientSecret == "" {
		log.Println("警告：client_id 或 client_secret 为空，请在 config.json 中填写")
	}
	// redirect_uri 校验：必须退出。
	// 原因：NewServer 会用它的「路径部分」决定回调接口挂在哪个路由上，
	// 为空时程序根本不知道要监听哪个回调路径，继续运行毫无意义。
	if cfg.PDD.RedirectURI == "" {
		log.Fatalf("redirect_uri 未配置")
	}

	// 初始化 token 存储。
	// 作用：把「商家 owner_id → 授权凭证」的映射持久化到本地 JSON 文件，
	// 使进程重启后无需让商家重新授权。
	// 该构造函数会顺带创建父目录；文件不存在时按空存储处理，不算错误。
	tokens, err := NewTokenStore(cfg.TokenStoreFile)
	if err != nil {
		log.Fatalf("初始化 token 存储失败: %v", err)
	}

	// 创建拼多多 API 客户端。
	// 它封装了公共参数拼装、MD5 签名、HTTP 调用与错误响应判定，
	// 后续所有对拼多多的请求（换 token、刷 token、查订单）都经它发出。
	pdd := NewPddClient(cfg.PDD.ClientID, cfg.PDD.ClientSecret, cfg.PDD.Gateway)

	// 构造 HTTP 服务对象，把配置、客户端、存储注入进去。
	// NewServer 内部会解析 redirect_uri，其路径部分即回调接口的路由。
	srv, err := NewServer(cfg, pdd, tokens)
	if err != nil {
		log.Fatalf("初始化服务失败: %v", err)
	}

	// 打印启动信息，方便本地调试时直接复制链接。
	// 注意：0.0.0.0 只表示「监听所有网卡」，浏览器访问应使用 localhost 或真实域名。
	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	log.Printf("监听地址: http://%s", addr)
	log.Printf("授权入口: http://%s/authorize", addr)
	log.Printf("回调地址: %s（请与控制台配置保持一致）", cfg.PDD.RedirectURI)

	// 注册路由并启动监听。
	// srv.routes() 返回一个挂载了全部接口的 http.Handler。
	// 该调用是阻塞的：只有监听失败（端口占用、权限不足等）才会返回非 nil 错误。
	if err := http.ListenAndServe(addr, srv.routes()); err != nil {
		log.Fatalf("HTTP 服务启动失败: %v", err)
	}
}
