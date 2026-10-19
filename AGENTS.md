# AGENTS.md

本文件是本仓库的协作约定，适用于**所有**在此仓库工作的 AI Agent 与人类开发者。
进入仓库后请先读完本文件，再动手改代码。

---

## 一、最高优先级：每次变更的强制流程

> **任何改动（代码、配置、文档、资源）都必须走完下面 4 步，不允许只改不提交、只提交不推送。**

### 第 1 步：改代码的同时更新文档

改动落地后，**立刻**判断影响哪份文档并同步更新，不许留到"下次一起改"：

| 改动类型 | 必须同步的文档 |
| --- | --- |
| 新增/修改/删除 HTTP 接口、参数、返回结构 | `使用文档.md` 第 7 节「接口清单」 |
| 新增/修改配置项（`config.json` 字段） | `使用文档.md` 第 4 节「配置说明」+ `README.md` 配置说明 |
| 改动授权 / token / 刷新逻辑 | `使用文档.md` 第 6 节 + `小白教程-Token有效期与自动刷新.md` |
| 改动调用链、模块职责、跨站交互 | `调用关系时序图.html`（对应时序图需要重画） |
| 新增文件 / 调整目录结构 | `使用文档.md` 第 2 节「目录结构」、`README.md` |
| 改动依赖、构建方式、启动命令 | `README.md` + `Makefile` + 本文件「常用命令」 |
| 修复了一个会让人踩坑的 bug | `使用文档.md` 第 10 节「常见问题排查」 |

判断标准：**一个新人只看文档，能不能跑通。** 不能，就是文档没更新完。

### 第 2 步：自检

```bash
make fmt     # 或 go fmt ./...
make build   # 必须编译通过
make test    # 必须测试通过
```

Windows 无 make 时：

```powershell
go fmt ./...
go build -o bin\pdd-callback-test.exe .
go test ./...
```

**编译不过、测试不过，不许提交。**

### 第 3 步：提交（commit）

```bash
git add -A
git commit -m "<type>: <中文简述>"
```

要求：

- 一个提交只做**一件事**。代码和它的文档更新放在**同一个提交**里。
- 提交信息用**中文**，遵循 `<type>: <简述>` 格式。
- 正文写清「为什么改」和「影响面」，不要只写「update」「test」这种无信息量的信息。

### 第 4 步：推送到远程（必须做）

```bash
git push origin main
```

**提交完必须推送，不允许把提交留在本地。**
推送后确认状态干净：

```bash
git status -sb   # 应显示 ## main...origin/main，且无 ahead/behind
```

### 一键走完（确认自检通过后）

```bash
go fmt ./... && go build -o bin/pdd-callback-test.exe . && go test ./... \
  && git add -A && git commit -m "<type>: <简述>" && git push origin main && git status -sb
```

---

## 二、项目简介

拼多多开放平台「企业ERP」回调地址最小可运行示例。

- 语言：Go（仅标准库，**无第三方依赖**，请勿随意引入）
- 入口：`main.go`，HTTP 服务监听 `:8080`
- 核心闭环：商家授权 → 回调收 `code` → 换 `access_token` → 调业务接口（订单查询）
- token 落库：`data/tokens.json`（已在 `.gitignore`，不入库）

## 三、目录结构与模块职责

```text
pdd-callback-test/
├── main.go          # 入口：读配置 → 建 token 存储 → 建 PddClient → 建 Server → 监听
├── config.go        # 配置结构与默认值填充
├── config.json      # 配置文件（⚠️ 不许提交真实密钥，见「安全红线」）
├── pdd.go           # 拼多多客户端：签名 sign()、doRequest()、换/刷 token、订单
├── server.go        # 路由注册与 7 个 handler
├── store.go         # TokenStore（落盘 JSON）+ StateStore（内存，10 分钟 TTL）
├── server_test.go   # httptest 测试
├── Makefile
├── README.md                     # 快速上手
├── 使用文档.md                    # 完整使用说明（接口、配置、排查）
├── 小白教程-Token有效期与自动刷新.md  # token 生命周期专题
├── 调用关系时序图.html             # 4 张调用/通信时序图
└── AGENTS.md                     # 本文件
```

## 四、常用命令

| 命令 | 作用 |
| --- | --- |
| `make run` / `go run . -config config.json` | 启动服务 |
| `make build` | 编译到 `bin/` |
| `make fmt` | 格式化 |
| `make test` | 跑测试 |
| `make clean` | 清理产物 |

## 五、代码约定

- **中文**写注释、文档、提交信息。
- 所有外发请求必须走 `PddClient.doRequest`，不要另起 `http.Post` —— 签名、超时（10s）、`error_response` 判定都在里面统一处理。
- 新增拼多多接口：在 `pdd.go` 加方法，复用 `doRequest`，只传不同的 `type` 与业务参数。
- 新增 HTTP 接口：在 `server.go` 的 `routes()` 注册，handler 命名 `handleXxx`，**同步更新 `使用文档.md` 第 7 节**。
- 时间一律用秒级 Unix 时间戳（`int64`），与 token 字段保持一致。
- 不要写死 token 有效期数字，一律以接口返回的 `expires_in` / `expires_at` 为准。

## 六、安全红线（违反即事故）

1. **`config.json` 里禁止出现真实的 `client_id` / `client_secret`**，必须留空；本地调试请在本地填写，**不要提交**。
2. 不要提交 `data/`（token 明文）、`bin/`、`.workbuddy/` —— 均已在 `.gitignore`。
3. `client_secret`、`access_token`、`refresh_token` **禁止写进日志、禁止出现在接口返回的完整值里**（`/tokens` 只回打码值）。
4. 回调地址生产环境必须 HTTPS；`state` 防 CSRF 逻辑不许改成固定值或绕过。

## 七、本项目的已知坑（改代码前先看）

- **重复授权会让旧的 `code` / `access_token` / `refresh_token` 立即全部失效**（拼多多官方规则）。调试时不要反复点「点击开始商家授权」，否则会误判成程序 bug。
- 同一个 `code` 换多次 token 结果一致，可安全重试。
- `code` 有效期 10 分钟，`state` 有效期 10 分钟，二者都短，别拖。
- 刷新后 `refresh_token` 会变，**必须覆盖落库**，否则新钥匙丢失。
- 企业ERP 属「三方工具」，`access_token` 有效期 **24 小时**，不存在永久 token。
