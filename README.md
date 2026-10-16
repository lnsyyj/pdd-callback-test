# pdd-callback-test

拼多多开放平台「企业ERP」回调地址最小可运行示例（Go 标准库实现，无第三方依赖）。

## 功能

- `GET /` 首页，含授权入口
- `GET /authorize` 生成授权链接并跳转到拼多多
- `GET /pdd/oauth/callback` 授权回调：校验 state → code 换 token → 本地落库
- `GET /orders?owner_id=xxx` 用已授权 token 查询订单（演示回调成功后调用业务接口）
- `GET /tokens` 查看已授权商家（token 打码显示）
- `GET /healthz` 健康检查

## 快速开始

1. 修改 `config.json`：填入 `client_id`、`client_secret`，并把 `redirect_uri` 设为与控制台一致的回调地址。
   - 本地调试可用 `http://localhost:8080/pdd/oauth/callback`；
   - 真实授权需改成公网 HTTPS 地址，并与拼多多控制台配置完全一致。
2. 启动：
   ```bash
   make run
   ```
   或直接 `go run . -config config.json`
3. 浏览器打开 `http://localhost:8080`，点「点击开始商家授权」。
4. 在拼多多完成授权后，会跳回回调地址并保存 token；再访问 `/orders?owner_id=<owner_id>` 查询订单。

## 构建

```bash
make build   # 产物在 bin/pdd-callback-test(.exe)
make fmt
make test
make clean
```

Windows 上没有 make 时，可直接使用：
```powershell
go build -o bin\pdd-callback-test.exe .
```

## 配置说明

见 `config.json`：

- `server.host` / `server.port`：监听地址与端口
- `pdd.client_id` / `pdd.client_secret`：应用密钥
- `pdd.redirect_uri`：回调地址，必须与拼多多控制台一致
- `pdd.gateway` / `pdd.auth_url`：网关与授权页，默认已填
- `token_store_file`：token 落库文件（默认 `data/tokens.json`）

> 说明：示例为本地测试方便，token 以明文 JSON 落库，`state` 存内存。生产环境请改为加密存储 + Redis/Session，并注意接口名、参数与签名以拼多多官方文档为准。
