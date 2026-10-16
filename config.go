package main

import (
	"encoding/json"
	"os"
)

type Config struct {
	Server         ServerConfig `json:"server"`
	PDD            PDDConfig    `json:"pdd"`
	TokenStoreFile string       `json:"token_store_file"`
	LogLevel       string       `json:"log_level"`
}

type ServerConfig struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

type PDDConfig struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RedirectURI  string `json:"redirect_uri"`
	Gateway      string `json:"gateway"`
	AuthURL      string `json:"auth_url"`
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg.Server.Host == "" {
		cfg.Server.Host = "0.0.0.0"
	}
	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8080
	}
	if cfg.PDD.Gateway == "" {
		cfg.PDD.Gateway = "https://gw-api.pinduoduo.com/api/router"
	}
	if cfg.PDD.AuthURL == "" {
		cfg.PDD.AuthURL = "https://mms.pinduoduo.com/open.html"
	}
	if cfg.TokenStoreFile == "" {
		cfg.TokenStoreFile = "data/tokens.json"
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
	}
	return &cfg, nil
}
