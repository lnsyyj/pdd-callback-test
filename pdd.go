// 本文件是「拼多多 API 客户端」，把所有与开放平台网关打交道的事情收口在一处：
//
//   - 公共参数拼装（type / client_id / timestamp / data_type / version）；
//   - 请求签名（MD5 大写，见 sign）；
//   - 统一的 HTTP 调用与错误判定（见 doRequest）；
//   - 三个业务方法：code 换 token、刷新 token、查询订单。
//
// 为什么统一收口：拼多多的签名规则、错误返回结构对所有接口都一样，
// 若每个接口各写一套 HTTP 调用，签名细节极易写错且难以统一维护。
// 新增接口时只需在 doRequest 之上加一个薄方法（参照 ExchangeToken/OrderList）。
package main

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// PddClient 是调用拼多多开放平台 API 的客户端。
//
// 一个实例对应一个应用（一组 client_id/client_secret），可被所有 goroutine 并发复用，
// 因为它只在构造时写入字段，运行期只读。
type PddClient struct {
	// ClientID 是应用 client_id，作为公共参数随每个请求发送。
	ClientID string

	// ClientSecret 是应用密钥，仅用于本地计算签名，不会随请求明文发送。
	ClientSecret string

	// Gateway 是网关地址，所有接口都 POST 到这一个地址，靠 type 参数区分接口。
	Gateway string

	// HTTP 是底层 HTTP 客户端，统一设置了 10 秒超时，
	// 避免拼多多接口无响应时把本地请求一直挂住。
	HTTP *http.Client
}

// NewPddClient 创建拼多多 API 客户端。
//
// 参数：
//   - clientID：应用 client_id，来自开放平台控制台；
//   - clientSecret：应用密钥，用于签名计算；
//   - gateway：网关地址。传空字符串时自动使用官方默认网关
//     "https://gw-api.pinduoduo.com/api/router"。
//
// 返回值：
//   - *PddClient：可直接使用的客户端实例（永远不为 nil，不返回 error，
//     因为构造过程只做赋值，不涉及 IO）。
//
// 说明：这里固定了 10 秒超时。若业务有大分页查询（如订单全量拉取），
// 可能需要按场景放宽，可考虑改为可配置项。
func NewPddClient(clientID, clientSecret, gateway string) *PddClient {
	// 允许调用方省略网关地址，方便测试与切换沙箱环境。
	if gateway == "" {
		gateway = "https://gw-api.pinduoduo.com/api/router"
	}
	return &PddClient{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Gateway:      gateway,
		// Timeout 覆盖连接、重定向与读取响应的全过程。
		HTTP: &http.Client{Timeout: 10 * time.Second},
	}
}

// ErrorResponse 是拼多多网关的错误返回结构。
//
// 网关在业务失败时不会用 HTTP 4xx/5xx，而是返回 HTTP 200 + 一个 error_response 字段，
// 因此必须解析这个结构才能知道调用是否真的成功。当前实现直接把它序列化成错误返回，
// 未按 ErrorCode 做细分处理。
type ErrorResponse struct {
	// ErrorCode 是错误码，0 一般表示成功（本结构只在出错时被解析）。
	ErrorCode int64 `json:"error_code"`

	// ErrorMsg 是错误描述，可直接展示给开发者排查。
	ErrorMsg string `json:"error_msg"`
}

// Token 是拼多多「换取/刷新 token」接口的返回结构。
//
// 注意区分：本结构只用于解析接口响应，不落库；
// 本地持久化用的是 store.go 中的 TokenRecord（多了 FetchedAt 等字段）。
type Token struct {
	// AccessToken 是访问令牌，调用业务接口时必须携带。敏感，勿写日志。
	AccessToken string `json:"access_token"`

	// RefreshToken 是刷新令牌，用于在 access_token 过期前换新。敏感。
	// 拼多多采用滚动刷新：刷新后本字段会变，必须用新值覆盖旧值。
	RefreshToken string `json:"refresh_token"`

	// OwnerID 是授权商家的唯一标识，也是本地存储的主键。
	OwnerID string `json:"owner_id"`

	// OwnerName 是商家名称，仅用于展示。
	OwnerName string `json:"owner_name"`

	// Scope 是本次授权得到的接口权限范围。
	Scope []string `json:"scope"`

	// ExpiresIn 是 access_token 的有效时长（秒）。
	// 不同应用类型/状态取值不同（企业ERP 为 86400，即 24 小时），
	// 因此业务代码必须读这个值，不能假设固定。
	ExpiresIn int64 `json:"expires_in"`
}

// Order 是订单列表中的单条订单（仅示例所需字段）。
//
// 说明：拼多多订单接口返回的字段远多于此，本示例只取几个便于展示的字段；
// 需要更多字段时，按官方文档在此结构体上追加即可（json 标签与返回值一致）。
type Order struct {
	// OrderSn 是订单编号，商家侧的业务主键。
	OrderSn string `json:"order_sn"`

	// OrderStatus 是订单状态码（整数枚举），具体含义见官方文档。
	OrderStatus int `json:"order_status"`

	// GoodsAmount 是商品金额，单位为「分」，展示时需除以 100。
	GoodsAmount int64 `json:"goods_amount"`

	// ReceiverName 是收件人姓名。属于用户隐私，注意脱敏与合规。
	ReceiverName string `json:"receiver_name"`

	// ReceiverPhone 是收件人电话。属于用户隐私，注意脱敏与合规。
	ReceiverPhone string `json:"receiver_phone"`
}

// sign 计算拼多多开放平台要求的请求签名（MD5，结果转大写）。
//
// 签名算法（严格按下列顺序，任一步错都会导致网关报签名错误）：
//  1. 收集所有参数，剔除 sign 自身，以及「值为空字符串」的参数；
//  2. 按参数名做字典序升序排序；
//  3. 把每个参数拼成「名 + 值」的形式依次首尾相接（中间不加 & 或 =）；
//  4. 在拼接结果末尾追加 client_secret；
//  5. 对整个字符串做 MD5，取十六进制小写后再转大写。
//
// 参数：
//   - params：待签名的全部参数（键为参数名，值为参数值的字符串形式）。
//     典型的公共参数有 type、client_id、timestamp、data_type、version、access_token，
//     外加各接口自己的业务参数。不需要事先放入 sign，函数会自行排除。
//   - clientSecret：应用密钥，拼接到末尾参与摘要。
//
// 返回值：
//   - 32 位大写十六进制字符串，可直接作为 sign 参数发送。
//
// 注意事项：
//   - 空值参数是否参与签名，官方不同接口/时期的表述略有差异，本示例统一跳过空值；
//     若遇到签名不匹配，优先怀疑这一点。
//   - 拼接时不要给值加引号、不要做 URL 转义，必须是原始值。
//   - 不要把 client_secret 放在开头，它只加在末尾。
func sign(params map[string]string, clientSecret string) string {
	// 步骤 1：挑出参与签名的参数名。跳过 sign 本身（避免自我引用）和空值。
	keys := make([]string, 0, len(params))
	for k, v := range params {
		if k == "sign" || v == "" {
			continue
		}
		keys = append(keys, k)
	}

	// 步骤 2：按参数名字典序升序排列，保证签名结果稳定。
	sort.Strings(keys)

	// 步骤 3：依次拼接「参数名 + 参数值」。
	// 使用 strings.Builder 避免多次字符串拼接产生的内存拷贝。
	var sb strings.Builder
	for _, k := range keys {
		sb.WriteString(k)
		sb.WriteString(params[k])
	}

	// 步骤 4：末尾追加密钥。
	sb.WriteString(clientSecret)

	// 步骤 5：MD5 → 十六进制 → 大写。
	sum := md5.Sum([]byte(sb.String()))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// doRequest 是所有拼多多接口的统一出口：拼装公共参数、签名、发送、解析、判错。
//
// 参数：
//   - apiType：接口名，即请求中的 type 参数，例如 "pdd.order.list.get"。
//   - accessToken：访问令牌。传空字符串表示该接口不需要用户授权
//     （换 token、刷 token 属于此类）；非空时会作为公共参数发送。
//   - bizParams：该接口特有的业务参数。值为空字符串的项会被跳过，不会发送。
//
// 返回值：
//   - map[string]any：网关返回的 JSON 反序列化结果，供调用方按接口名取子字段；
//   - error：以下任一情况返回错误：
//   - 网络层失败（DNS、连接超时、10 秒超时等）；
//   - 读取响应体失败；
//   - HTTP 状态码不是 200；
//   - 响应体不是合法 JSON；
//   - 响应中包含 error_response 字段（业务失败）。
//
// 副作用：发起一次网络请求。本方法对调用方透明地完成签名，调用方无需关心。
//
// 请求形式：application/x-www-form-urlencoded 的 POST，
// 所有参数（公共 + 业务 + sign）都放在表单体里。
func (c *PddClient) doRequest(apiType, accessToken string, bizParams map[string]string) (map[string]any, error) {
	// 1. 先放公共参数。这些是每个接口都必带的。
	params := map[string]string{
		"type":      apiType,                                  // 接口名，决定网关路由到哪个 API
		"client_id": c.ClientID,                               // 应用标识
		"timestamp": strconv.FormatInt(time.Now().Unix(), 10), // 秒级时间戳，网关会校验时效性
		"data_type": "JSON",                                   // 要求以 JSON 返回
		"version":   "V1",                                     // 接口版本
	}

	// 2. 需要用户授权的接口才带 access_token。
	// 换 token / 刷 token 时还没有有效 token，传空即不加入参数。
	if accessToken != "" {
		params["access_token"] = accessToken
	}

	// 3. 合并业务参数。空值不发送，避免出现 "page=" 这类无意义参数影响签名。
	for k, v := range bizParams {
		if v != "" {
			params[k] = v
		}
	}

	// 4. 计算签名。必须在所有参数（含业务参数）都就位之后进行，
	//    且签名本身不能参与签名计算（sign 函数内部已排除）。
	params["sign"] = sign(params, c.ClientSecret)

	// 5. 转成表单。url.Values.Set 会自动做 URL 编码。
	form := url.Values{}
	for k, v := range params {
		form.Set(k, v)
	}

	// 6. 发送请求。PostForm 会设置 Content-Type 为 application/x-www-form-urlencoded。
	resp, err := c.HTTP.PostForm(c.Gateway, form)
	if err != nil {
		return nil, err
	}
	// 必须关闭响应体，否则连接无法复用，长期运行会耗尽句柄。
	defer resp.Body.Close()

	// 7. 一次性读完响应体。响应通常很小，无需流式处理。
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// 8. 非 200 视为网关层异常（如 502、限流页），把响应体一并带出便于排查。
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("网关返回 HTTP %d: %s", resp.StatusCode, string(body))
	}

	// 9. 解析 JSON。注意拼多多的业务错误也是 HTTP 200，
	//    所以「HTTP 成功」不等于「业务成功」，还要看下一步。
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}

	// 10. 业务错误判定：响应里只要出现非 nil 的 error_response，就说明调用失败。
	// 这里把该字段原样序列化成字符串返回，保留 error_code / error_msg 供排查。
	if er, ok := data["error_response"]; ok && er != nil {
		b, _ := json.Marshal(er)
		return nil, errors.New(string(b))
	}
	return data, nil
}

// ExchangeToken 用授权回调拿到的 code 换取 access_token / refresh_token。
//
// 参数：
//   - code：拼多多回跳时带在 URL 上的授权码。有效期仅 10 分钟，且一次性；
//     但「同一个 code 多次换取」得到的结果是一致的，因此网络超时后可安全重试。
//
// 返回值：
//   - *Token：包含 access_token、refresh_token、owner_id、expires_in 等；
//   - error：网络失败，或网关返回 error_response（如 code 已过期、已使用、
//     redirect_uri 与控制台不一致）。
//
// 副作用：发起一次网络请求；不会修改本地存储（落库由调用方负责）。
func (c *PddClient) ExchangeToken(code string) (*Token, error) {
	// 换 token 不需要 access_token（此时还没有），所以第二个参数传空。
	data, err := c.doRequest("pdd.pop.auth.token.create", "", map[string]string{"code": code})
	if err != nil {
		return nil, err
	}
	// 网关把结果包在以接口名命名的字段里，需要按名取出。
	return parseToken(data, "pdd_pop_auth_token_create_response")
}

// RefreshToken 用 refresh_token 换取新的 access_token。
//
// 参数：
//   - refreshToken：本地保存的刷新令牌，来自上一次授权或上一次刷新。
//
// 返回值：
//   - *Token：新的 access_token 与新的 refresh_token（注意后者会变，必须覆盖存储）；
//   - error：网络失败，或 refresh_token 已过期/已失效（此时只能让商家重新授权）。
//
// 重要：拼多多采用滚动刷新，刷新成功后旧的 refresh_token 通常会失效。
// 因此拿到新值后要立刻落库，否则可能把唯一的一把「新钥匙」丢掉。
//
// 副作用：发起一次网络请求。
func (c *PddClient) RefreshToken(refreshToken string) (*Token, error) {
	// 刷新同样不需要 access_token，只需 refresh_token。
	data, err := c.doRequest("pdd.pop.auth.token.refresh", "", map[string]string{"refresh_token": refreshToken})
	if err != nil {
		return nil, err
	}
	return parseToken(data, "pdd_pop_auth_token_refresh_response")
}

// parseToken 从网关返回的 map 中取出指定字段并解析成 Token。
//
// 参数：
//   - data：doRequest 返回的完整响应 map；
//   - key：承载 token 的字段名。换 token 与刷 token 的字段名不同：
//     "pdd_pop_auth_token_create_response" / "pdd_pop_auth_token_refresh_response"，
//     所以用参数传入而不写死。
//
// 返回值：
//   - *Token：解析结果；
//   - error：字段缺失、字段为 null，或字段内容类型不匹配时返回错误。
//
// 实现说明：使用「先 serialize 再 deserialize」的写法，
// 是为了绕开 map[string]any 逐字段断言的繁琐与易错，直接复用结构体的 json 标签。
//
// 兜底逻辑：若 expires_in 缺失或非正数（例如接口改版未返回该字段），
// 按 86400 秒（24 小时）估值。这只是防御性兜底，正常情况应以返回值准。
func parseToken(data map[string]any, key string) (*Token, error) {
	raw, ok := data[key]
	if !ok || raw == nil {
		return nil, fmt.Errorf("响应中缺少 %s 字段", key)
	}

	// 先转成 JSON 字节，再反序列化到结构体，省去手工类型断言。
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var token Token
	if err := json.Unmarshal(b, &token); err != nil {
		return nil, err
	}

	// 兜底：expires_in 缺失或非法时按 24 小时处理，避免后续计算出「已过期」。
	if token.ExpiresIn <= 0 {
		token.ExpiresIn = 86400
	}
	return &token, nil
}

// OrderList 分页查询指定商家的订单列表（示例用的业务接口）。
//
// 参数：
//   - accessToken：该商家的访问令牌，必须是已授权且未过期的；
//   - page：页码，从 1 开始（由调用方保证，本方法不做校验）；
//   - pageSize：每页条数，由调用方保证合理范围（本方法不做校验）。
//
// 返回值：
//   - []Order：当前页订单；无数据时为 nil，调用方按空切片处理更安全；
//   - int：符合条件的订单总数，可用于计算总页数；
//   - error：网络失败、鉴权失败（token 过期/无效）、无接口权限，或响应结构缺少预期字段。
//
// 副作用：发起一次网络请求。
func (c *PddClient) OrderList(accessToken string, page, pageSize int) ([]Order, int, error) {
	// 分页参数必须以字符串形式传给网关，因此这里显式转换。
	data, err := c.doRequest("pdd.order.list.get", accessToken, map[string]string{
		"page":      strconv.Itoa(page),
		"page_size": strconv.Itoa(pageSize),
	})
	if err != nil {
		return nil, 0, err
	}

	// 按接口名取出结果字段。缺失通常意味着接口名写错或权限不足。
	raw, ok := data["pdd_order_list_get_response"]
	if !ok || raw == nil {
		return nil, 0, errors.New("响应中缺少 pdd_order_list_get_response 字段")
	}

	// 同样用「序列化 → 反序列化」的方式映射到匿名结构体，
	// 只取需要的两个字段：列表与总数。
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, 0, err
	}
	var resp struct {
		OrderList  []Order `json:"order_list"`  // 当前页订单列表
		TotalCount int     `json:"total_count"` // 订单总数
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		return nil, 0, err
	}
	return resp.OrderList, resp.TotalCount, nil
}
