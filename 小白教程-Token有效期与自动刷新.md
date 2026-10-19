# 小白教程：token 能有多久？会过期吗？能不能永久？

> 面向第一次接拼多多开放平台的同学。跟着做完，你能**亲眼看到**自己应用的 token 到底多久过期、过期前后程序做了什么。
>
> 适用项目：pdd-callback-test（企业ERP 类应用示例）
> 规则来源：拼多多开放平台官方《授权说明》 https://open.pinduoduo.com/application/document/browse?idStr=BD3A776A4D41D5F5

---

## 0. 先给结论（没耐心就看这段）

| 问题 | 答案 |
| --- | --- |
| 能申请更长的有效期吗？ | **不能自己"申请"**。有效期由平台按"应用标签 + 应用状态"定死，你没有可填的选项。 |
| 最长能有多久？ | **1 年**（只有"二方系统"且"已上线"才有）；上架服务市场的按订购时长算。 |
| 本项目的「企业ERP」是多久？ | **24 小时**（86400 秒）。审核通过、已上线都是 24 小时。 |
| 可以永久吗？ | **不可以。拼多多没有任何"永久 token"。** 到期就得刷新，刷新期限用完就得让商家重新授权。 |
| 那我要天天盯吗？ | 不用。刷新是程序自动做的，本项目已经内置了。 |

**一句话记住：token 一定会过期，代码里永远不要假设它长期有效。**

---

## 1. 官方规则原文（唯一可信来源）

官方把有效期分成 **3 个应用标签 × 3 个应用状态**：

| 应用状态 | 三方工具 | 二方系统 | 新业务应用 |
| --- | --- | --- | --- |
| 创建、审核通过 | 24 小时 | 24 小时 | 24 小时 |
| 已上线 | **24 小时** | **1 年** | **1 年** |
| 已发布服务市场 | 同订购时长 | - | - |

官方对"三方工具"的定义里，明确包含这些应用类型：

```text
卡券商工具、多多推广、短信服务、打单、进销存、商品优化、跨境企业ERP、
企业ERP、物流仓储系统、Knock应用、订单处理
```

👉 **本项目是「企业ERP」，属于"三方工具"，所以不管审核通过还是已上线，都是 24 小时。**
👉 「二方系统」指的是商家自研的商家后台系统，那类才有 1 年。

官方还特别说明了一句，很多人没注意：

> 授权时长以获取 access_token 接口返回字段 **expires_at 及 expires_in 的差值秒数为准**。

意思是：**别背数字，以接口返回值为准**。数字随时可能调整，你的代码必须读返回值。

---

## 2. 刷新是怎么规定的（关键，别理解错）

官方原文（《授权说明》刷新 token 章节）：

> 可以通过 pdd.pop.auth.token.refresh 接口，但**刷新授权不会延长 access_token 的有效期**，请求参数如下，**refresh_token 有效时间同授权时长**，有效期内均可以请求刷新 access_token。

拆开讲，三个要点：

1. **刷新 ≠ 永久续命。** 刷新只是"换一张新票"，不是把原来的票改长。
2. **refresh_token 的寿命和授权时长一样。** 企业ERP 就是 24 小时。过了这个窗口，refresh_token 也废了，只能让商家**重新授权**。
3. **刷新会换新的 refresh_token。** 每次刷新返回的 `refresh_token` 可能和上次不一样，**必须覆盖存起来**，不能还用旧的。

对应的接口：

| 动作 | 接口（type 参数） | 什么时候用 |
| --- | --- | --- |
| code 换 token | `pdd.pop.auth.token.create` | 商家第一次授权回调时 |
| 刷新 token | `pdd.pop.auth.token.refresh` | access_token 快过期时 |
| 查订单（业务接口） | `pdd.order.list.get` | 日常调用 |

---

## 3. 为什么你在网上会看到 2 小时 / 7 天 / 30 天

因为大量博客是抄的、过时的、或者写的是别家平台（京东 2 小时、抖店 24 小时之类混在一起）。

**判断标准很简单：只要不是 open.pinduoduo.com 上的原文，就当参考，不当依据。**

而且注意，同一个平台不同应用类型有效期本来就不同，所以"拼多多 token 有效期是 X"这种说法本身就是错的——它取决于你的应用标签和状态。

---

## 4. 跟着做：5 步验证你的 token 到底多久过期

> 前置条件：你已经按《使用文档》填好 `config.json`，并且能跑起来。
> 只用一台电脑即可，命令用 Git Bash 或 PowerShell 都行。

### 第 1 步：启动程序并完成一次授权

```bash
go run . -config config.json
```

浏览器打开 `http://localhost:8080`，点「点击开始商家授权」，在拼多多页面同意授权。

**你应该看到**：浏览器跳到「授权成功」页，显示商家名和 `owner_id`。

---

### 第 2 步：看这个 token 的真实有效期

另开一个终端，查已授权商家：

```bash
curl -s http://localhost:8080/tokens
```

返回类似：

```json
{
  "count": 1,
  "tokens": [
    {
      "owner_id": "123456",
      "owner_name": "某某旗舰店",
      "access_token_masked": "abcd****wxyz",
      "expires_at": 1793000000
    }
  ]
}
```

`expires_at` 是**过期时刻的 Unix 时间戳（秒）**。算一下还剩多久：

**Git Bash：**

```bash
EXP=$(curl -s http://localhost:8080/tokens | grep -o '"expires_at":[0-9]*' | head -1 | cut -d: -f2)
NOW=$(date +%s)
echo "还剩 $(( (EXP - NOW) / 60 )) 分钟，约 $(( (EXP - NOW) / 3600 )) 小时"
```

**PowerShell：**

```powershell
$t = (Invoke-RestMethod http://localhost:8080/tokens).tokens[0]
$left = $t.expires_at - [DateTimeOffset]::UtcNow.ToUnixTimeSeconds()
"还剩 {0} 分钟，约 {1} 小时" -f [math]::Floor($left/60), [math]::Round($left/3600, 1)
```

**你应该看到**：企业ERP 应该是 **1440 分钟左右（≈24 小时）**。

> 如果你看到的是 525600 分钟（≈1 年），说明你的应用被划到了「二方系统」，恭喜，你几乎不用操心刷新。
> 如果看到的是 120 分钟（2 小时），那大概率是别家平台的教程串味了，请回控制台确认应用类型。

---

### 第 3 步：看落库的原始字段

```bash
cat data/tokens.json
```

**你应该看到**（已脱敏）：

```json
{
  "123456": {
    "owner_id": "123456",
    "owner_name": "某某旗舰店",
    "access_token": "40e6****5caf8",
    "refresh_token": "6a2b****e53fc",
    "scope": [],
    "expires_in": 86400,
    "fetched_at": 1792893600
  }
}
```

关系是死的：

```text
expires_at = fetched_at + expires_in
```

- `expires_in`：**有效期长度**（秒），拼多多返回多少就是多少
- `fetched_at`：**拿到这个 token 的时刻**（本程序写入的时间戳）
- `expires_at`：两者相加，即过期时刻

> `86400` 就是 24 小时。代码里 `if token.ExpiresIn <= 0 { token.ExpiresIn = 86400 }` 只是**兜底**，真正生效的是接口返回值。

---

### 第 4 步：亲手触发一次自动刷新（最重要的一步）

刷新逻辑藏得很深，我们把它"骗"出来。原理：程序判断 `expires_at <= 当前时间 + 300秒` 就会刷新。我们只要把 `fetched_at` 改小，就等价于"这个 token 快过期了"。

**① 先备份**

```bash
cp data/tokens.json data/tokens.json.bak
```

**② 记下当前的 access_token**（前 8 位就行），等下对比

**③ 把 fetched_at 改成 0**

用编辑器打开 `data/tokens.json`，把 `"fetched_at": 1792893600` 改成：

```json
"fetched_at": 0
```

**④ 调一次业务接口**

```bash
curl "http://localhost:8080/orders?owner_id=123456"
```

**⑤ 再看数据文件**

```bash
cat data/tokens.json
```

**你应该看到**：

- `access_token` **变了**（和刚才记的不一样）
- `refresh_token` **也变了**（这是拼多多的滚动刷新，正常现象）
- `fetched_at` 变成了**当前时间**
- `expires_at` 往后推了 24 小时

**这说明**：程序发现 token 快过期 → 自动调 `pdd.pop.auth.token.refresh` → 拿到新 token → 覆盖写回文件 → 再用新 token 查订单。全程你只调了一次 `/orders`，刷新是透明的。

---

### 第 5 步：看看 refresh_token 失效会怎样

**① 备份当前文件**

```bash
cp data/tokens.json data/tokens.json.ok
```

**② 把 refresh_token 改坏**

```json
"refresh_token": "invalid_token_for_test"
```

同样把 `"fetched_at": 0` 也改掉，保证它会去刷新。

**③ 再查订单**

```bash
curl "http://localhost:8080/orders?owner_id=123456"
```

**你应该看到**：`500` 错误，内容类似

```text
刷新 token 失败：{...error_response...}
```

**这说明**：refresh_token 真失效时，程序不会瞎撑，会直接报错。这时唯一的出路是——**重新走一遍授权**（浏览器打开 `/authorize`）。

**④ 收尾**：把备份还原，继续用正常 token

```bash
cp data/tokens.json.ok data/tokens.json
```

> ⚠️ 注意：如果第 4 步已经成功刷新过，`tokens.json.bak` 里那个 refresh_token 可能**已经失效**了，备份不能当"后悔药"用。

---

## 5. 时间换算速查

| 秒 | 等于 | 常见对应 |
| --- | --- | --- |
| 300 | 5 分钟 | 本程序的提前刷新缓冲 |
| 600 | 10 分钟 | 授权 code 有效期；本程序 state 有效期 |
| 3600 | 1 小时 | - |
| 86400 | 24 小时 | 企业ERP 的 access_token 有效期 |
| 604800 | 7 天 | - |
| 31536000 | 365 天 | 二方系统已上线的有效期 |

代码里到处是秒，先用这张表换算，别被 `86388` 这种数字绕晕（它就是 24 小时减去几十秒的余量）。

---

## 6. 三种"过期"别搞混

新手最容易把这几个混成一件事，其实完全独立：

| 东西 | 有效期 | 过期后果 | 怎么恢复 |
| --- | --- | --- | --- |
| 授权码 `code` | **10 分钟** | 换不到 token | 重新授权 |
| `access_token` | 24 小时（企业ERP） | 业务接口报错 | 用 refresh_token 刷新 |
| `refresh_token` | 同授权时长 | 刷新不了 | **重新授权**（没别的办法） |
| 本程序的 `state` | 10 分钟 | 回调报 400 | 重新点授权 |

一个常见误会：以为 refresh_token 能一直刷新下去。**不能。** 它的寿命和授权时长一样，用完就得让商家重新点授权。

---

## 7. 生产环境该怎么做（demo 不够用）

本程序为了好懂，刷新是"用的时候才刷"。真上线要补这几点：

1. **提前刷新**。本项目是提前 5 分钟（`now + 300`），生产建议提前 30 分钟，留出重试时间。
2. **定时任务兜底**。别等用户来访问才刷新。加个定时任务，每小时扫一遍即将过期的 token 主动刷新。
3. **刷新后立刻落库**。refresh_token 会滚动变化，没存下来等于把新钥匙丢了。
4. **加并发锁**。两个请求同时发现要刷新，会各自刷一次，后刷的把先刷的顶掉，先刷的那次就白费了。用 Redis 分布式锁，或按 owner_id 加互斥。
5. **刷新失败要能兜住**。记日志 + 通知商家重新授权，别让业务静默失败。
6. **token 加密存储**。本项目是明文 JSON，生产必须加密（AES / KMS）+ 按 owner_id 隔离。

---

## 8. 最容易踩的坑

**① 重复点授权，会把之前的 token 全部作废**

官方原话：

> 允许重复授权（当前存在频率限制，请勿密集重复授权），重复授权完成后，**历史已获取的 code、access_token 以及 refresh_token 均会立即失效**。

所以测试时**不要反复点**「点击开始商家授权」，否则你刚存下来的 token 立刻就废了，还以为是程序 bug。这是本项目调试时最容易被误判的现象。

**② 同一个 code 换多次是安全的**

官方原话：多次使用 code 换取 access_token，返回结果是一样的。所以网络超时后重试换 token 不会出问题，放心重试。

**③ 别把 86400 写死在业务代码里**

要用 `expires_in` / `expires_at` 算。平台一调整，写死的代码就集体故障。

**④ code 只有 10 分钟**

用户在授权页犹豫太久，回来就换不到 token 了，要提示他重新授权。

**⑤ 授权账号数量有上限**

企业ERP 创建审核通过时只能授权 5 个账号，已上线 25 个。测试够了再要名额。

---

## 9. 一张图记住整条命

```text
商家点授权
   ↓
拿到 code（10 分钟内有效）
   ↓
code 换 token → access_token（24h） + refresh_token（24h）
   ↓
业务调用，快过期（剩 5 分钟）时自动刷新
   ↓
刷新拿到新 access_token + 新 refresh_token（滚动，旧的失效）
   ↓
refresh_token 也过期了
   ↓
只能让商家重新授权 ← 没有"永久"这个选项
```

---

## 10. 一句话总结

**没有永久 token。企业ERP 是 24 小时一轮，靠 refresh_token 滚动续期；refresh_token 本身也是 24 小时的寿命，用完必须让商家重新授权。你的代码只认接口返回的 `expires_in`，别认数字。**
