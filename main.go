package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
)

func main() {
	configPath := flag.String("config", "config.json", "配置文件路径")
	flag.Parse()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("读取配置失败: %v", err)
	}

	if cfg.PDD.ClientID == "" || cfg.PDD.ClientSecret == "" {
		log.Println("警告：client_id 或 client_secret 为空，请在 config.json 中填写")
	}
	if cfg.PDD.RedirectURI == "" {
		log.Fatalf("redirect_uri 未配置")
	}

	tokens, err := NewTokenStore(cfg.TokenStoreFile)
	if err != nil {
		log.Fatalf("初始化 token 存储失败: %v", err)
	}

	pdd := NewPddClient(cfg.PDD.ClientID, cfg.PDD.ClientSecret, cfg.PDD.Gateway)
	srv, err := NewServer(cfg, pdd, tokens)
	if err != nil {
		log.Fatalf("初始化服务失败: %v", err)
	}

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	log.Printf("监听地址: http://%s", addr)
	log.Printf("授权入口: http://%s/authorize", addr)
	log.Printf("回调地址: %s（请与控制台配置保持一致）", cfg.PDD.RedirectURI)
	if err := http.ListenAndServe(addr, srv.routes()); err != nil {
		log.Fatalf("HTTP 服务启动失败: %v", err)
	}
}
