// 本文件负责「配置」这一件事：定义配置的数据结构，以及从 JSON 文件加载配置。
//
// 设计要点：
//   - 结构体字段与 config.json 的键名一一对应（通过 json 标签映射）；
//   - 所有非必填字段都可以在文件中省略，由 loadConfig 统一补默认值，
//     这样使用者只需要关心自己真正要改的那几项。
package main

import (
	"encoding/json"
	"os"
)

// Config 是程序的全部配置，与 config.json 一一对应。
//
// 配置采用两段式分层：server 段描述本机 HTTP 服务，pdd 段描述拼多多开放平台的
// 凭据与接口地址；另外两个平铺字段控制 token 存储位置与日志级别。
//
// 该结构体由 main 持有，并被 Server 引用（Server 通过它读取 redirect_uri、
// client_id、auth_url 等信息），因此运行期视为只读，不做并发修改。
type Config struct {
	// Server 是本机 HTTP 服务的监听配置（监听地址 + 端口）。
	Server ServerConfig `json:"server"`

	// PDD 是拼多多开放平台的应用凭据与接口地址。
	PDD PDDConfig `json:"pdd"`

	// TokenStoreFile 是授权得到的 token 的落库文件路径，默认 "data/tokens.json"。
	// 该文件内容为明文 JSON（以商家 owner_id 为键），生产环境应改为加密存储或数据库。
	TokenStoreFile string `json:"token_store_file"`

	// LogLevel 是预留字段，当前版本尚未参与日志级别过滤，填 "info" 即可。
	// 保留它是为了让配置文件结构在后续加日志分级时不必破坏兼容性。
	LogLevel string `json:"log_level"`
}

// ServerConfig 描述本机 HTTP 服务如何监听。
type ServerConfig struct {
	// Host 是监听地址。
	// "0.0.0.0" 表示监听本机所有网卡（局域网内其他机器也能访问）；
	// "127.0.0.1" 表示只允许本机访问，调试时更安全。默认 "0.0.0.0"。
	Host string `json:"host"`

	// Port 是监听端口，默认 8080。
	// 该端口必须与 redirect_uri 中声明的端口一致，否则拼多多回调打不进来。
	Port int `json:"port"`
}

// PDDConfig 描述拼多多开放平台的应用凭据与接口地址。
type PDDConfig struct {
	// ClientID 是应用（client_id），由拼多多开放平台在创建应用后分配。
	// 会作为公共参数出现在每个接口请求中，也会拼进授权链接。
	ClientID string `json:"client_id"`

	// ClientSecret 是应用密钥，用于请求签名（sign）计算，绝不能泄露。
	// 注意：真实密钥不要提交到代码仓库，本地填写后请勿 commit。
	ClientSecret string `json:"client_secret"`

	// RedirectURI 是授权回调地址，必须与拼多多控制台登记的完全一致
	// （协议、域名、端口、路径都要一致）。
	// 特殊作用：它的「路径部分」同时决定回调接口挂载在哪个路由上，
	// 例如填 https://erp.example.com/api/pdd/callback，则回调接口就是 /api/pdd/callback。
	RedirectURI string `json:"redirect_uri"`

	// Gateway 是拼多多 API 网关地址，所有接口请求都 POST 到这里，
	// 通过 type 参数区分具体调用哪个接口。默认 "https://gw-api.pinduoduo.com/api/router"。
	Gateway string `json:"gateway"`

	// AuthURL 是商家授权页地址，浏览器会被 302 跳转到这个页面完成登录与授权。
	// 默认 "https://mms.pinduoduo.com/open.html"。
	// 注意：这是「页面」地址，不是 API 地址，本地服务不会直接请求它。
	AuthURL string `json:"auth_url"`
}

// loadConfig 从指定路径读取 JSON 配置文件，解析成 Config，并补齐缺省字段的默认值。
//
// 参数：
//   - path：配置文件路径。可以是相对路径（相对进程当前工作目录）或绝对路径。
//
// 返回值：
//   - *Config：解析并补全默认值后的配置对象，调用方可直接使用；
//   - error：文件读不到、无权限、或 JSON 格式非法时返回对应错误。
//
// 副作用：只读取文件，不修改文件、不发起网络请求。
//
// 默认值策略（配置文件里省略这些字段也能正常启动）：
//   - server.host      → "0.0.0.0"（监听所有网卡，方便局域网/内网穿透调试）
//   - server.port      → 8080
//   - pdd.gateway      → "https://gw-api.pinduoduo.com/api/router"
//   - pdd.auth_url     → "https://mms.pinduoduo.com/open.html"
//   - token_store_file → "data/tokens.json"
//   - log_level        → "info"
//
// 注意：client_id、client_secret、redirect_uri 三项没有默认值，必须由使用者填写；
// 其中 redirect_uri 为空会让程序在 main 中直接退出。
func loadConfig(path string) (*Config, error) {
	// 一次性读入整个文件。配置文件很小，无需流式读取。
	data, err := os.ReadFile(path)
	if err != nil {
		// 文件不存在、路径错误、无读权限都会走到这里，原样返回错误即可，
		// 由调用方决定如何提示（main 中会打印「读取配置失败」并退出）。
		return nil, err
	}

	// 反序列化。未在文件中出现的字段保持 Go 零值，随后由下面的逻辑补默认值。
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		// JSON 语法错误或字段类型不匹配（例如 port 写成字符串）会在这里暴露。
		return nil, err
	}

	// 以下逐项补默认值：只在该字段「为零值」时才填，
	// 因此用户在配置文件里显式写的值永远优先，不会被覆盖。
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
