// 本文件负责「本地存储」，包含两个互不相关的存储组件：
//
//   - TokenStore：授权凭证存储。把「商家 owner_id → TokenRecord」持久化到 JSON 文件，
//     使进程重启后不必让商家重新授权。带互斥锁，可被多个 HTTP 请求并发访问。
//   - StateStore：OAuth state 存储。只存内存，用于防 CSRF 与防重放，
//     每个 state 一次性消费、10 分钟过期，进程重启后自动清空（这是可接受的）。
//
// 选型说明：示例为了「零第三方依赖 + 一眼看懂」，用 JSON 文件代替数据库、
// 用内存 map 代替 Redis。生产环境应替换为加密存储 + 数据库/Redis。
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// TokenRecord 是「一个商家的一次授权结果」，也是写入 JSON 文件的单条记录。
//
// 与 pdd.go 中的 Token 的区别：
//   - Token 是拼多多接口的原始返回结构，只用于解析响应；
//   - TokenRecord 是本地的持久化结构，额外记录了「什么时候拿到的」（FetchedAt），
//     以便日后推算过期时刻；不含与落库无关的字段。
//
// 同一个 owner_id 反复授权时，新记录会覆盖旧记录（见 TokenStore.Save）。
type TokenRecord struct {
	// OwnerID 是拼多多商家 ID，作为存储的主键。
	// 查询订单、刷新 token 都以它为索引，因此必须保证非空。
	OwnerID string `json:"owner_id"`

	// OwnerName 是商家名称（如「某某旗舰店」），仅用于展示与日志，不参与鉴权。
	OwnerName string `json:"owner_name"`

	// AccessToken 是调用业务接口时携带的访问令牌（有效期通常 24 小时）。
	// 敏感信息：接口对外展示时必须打码，禁止写日志。
	AccessToken string `json:"access_token"`

	// RefreshToken 是刷新访问令牌用的刷新令牌（有效期与授权时长一致）。
	// 敏感信息；且拼多多采用「滚动刷新」，每次刷新后必须用新值覆盖本字段。
	RefreshToken string `json:"refresh_token"`

	// Scope 是该次授权实际获得的权限范围（拼多多返回的接口权限列表）。
	// 本示例不校验它，仅原样保存以便排查「为什么某个接口没权限」。
	Scope []string `json:"scope"`

	// ExpiresIn 是 access_token 的有效时长，单位为秒，取自接口返回值。
	// 注意不要写死数字：不同应用类型/状态的有效期不同（企业ERP 为 86400 秒）。
	ExpiresIn int64 `json:"expires_in"`

	// FetchedAt 是「拿到这个 token 的时刻」，Unix 秒级时间戳。
	// 它与 ExpiresIn 相加即为过期时刻，见 ExpiresAt。
	FetchedAt int64 `json:"fetched_at"`
}

// ExpiresAt 计算该 token 的过期时刻。
//
// 返回值：
//   - Unix 秒级时间戳，等于 FetchedAt + ExpiresIn。
//
// 用途：handleOrders 用它判断是否需要提前刷新（剩余不足 5 分钟就刷新）。
//
// 注意：这里假设本地时钟准确。若服务器时钟偏差较大，可能导致过早或过晚刷新。
// 想更稳妥可以改用拼多多返回的 expires_at 绝对值字段。
func (t *TokenRecord) ExpiresAt() int64 {
	return t.FetchedAt + t.ExpiresIn
}

// TokenStore 是并发安全的 token 存储，内存 map + 本地 JSON 文件双写。
//
// 工作方式：
//   - 读操作（Get/All）只读内存，速度快；
//   - 写操作（Save）先改内存，再整体重写文件，保证重启后数据还在；
//   - 所有导出方法都加互斥锁，可安全地被多个 goroutine 同时调用。
//
// 并发说明：锁是「进程内」的。若同一份 tokens.json 被多个进程同时写入，
// 仍会互相覆盖，生产环境请改用数据库或加文件锁。
type TokenStore struct {
	// mu 保护 data，防止并发读写 map 导致 panic。
	// 注意：Save 期间会持有锁做磁盘 IO，属于「粗粒度锁」，
	// 本示例商家数量少，可以接受；量大时应改为读写锁 + 异步落盘。
	mu sync.Mutex

	// path 是 JSON 文件的路径，即新增/更新后的落盘位置。
	path string

	// data 是内存索引：key 为商家 owner_id，value 为该商家的授权记录。
	// 全量加载自 JSON 文件，之后以内存为准。
	data map[string]TokenRecord
}

// NewTokenStore 创建 token 存储，并把已存在的授权记录从文件加载到内存。
//
// 参数：
//   - path：JSON 文件路径，例如 "data/tokens.json"。相对路径相对进程工作目录。
//
// 返回值：
//   - *TokenStore：初始化完成的存储对象；
//   - error：目录创建失败、文件存在但读取失败、或文件内容不是合法 JSON 时返回错误。
//
// 副作用：
//   - 若 path 的父目录不存在，会以 0755 权限自动创建（避免首次启动因目录缺失而失败）；
//   - 会读取文件，但不会创建文件。文件在第一次 Save 时才被写出。
//
// 容错策略：
//   - 文件不存在 → 视为「还没有任何商家授权」，返回空存储而不是报错；
//   - 文件为空（0 字节） → 同样按空存储处理，常见于刚被 touch 出来的文件。
func NewTokenStore(path string) (*TokenStore, error) {
	// 先建一个空存储，后续无论从文件加载成功与否都以它为基础。
	ts := &TokenStore{path: path, data: map[string]TokenRecord{}}

	// 确保父目录存在。filepath.Dir 对 "tokens.json" 这类无目录的路径返回 "."，
	// 此时无需创建（当前目录一定存在），所以排除空串和 "."。
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}

	// 读取已有数据。首次运行时文件通常不存在，属于正常情况。
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// 首次启动：没有任何历史授权，直接返回空存储。
			return ts, nil
		}
		// 其他错误（无权限等）是真问题，向上抛出。
		return nil, err
	}
	if len(b) == 0 {
		// 空文件（例如手工创建后未写入内容）也按空存储处理。
		return ts, nil
	}

	// 反序列化到 map。格式非法时直接报错，避免带着「读不懂的旧数据」继续运行，
	// 否则后续 Save 会把旧记录整体覆盖掉，造成静默丢失。
	if err := json.Unmarshal(b, &ts.data); err != nil {
		return nil, err
	}
	return ts, nil
}

// Save 保存（或覆盖）一个商家的授权记录，并立即持久化到文件。
//
// 参数：
//   - rec：要保存的记录。以 rec.OwnerID 为唯一键，已存在则整条覆盖。
//
// 返回值：
//   - error：序列化或写文件失败时返回错误（此时内存已更新，但磁盘未更新）。
//
// 调用时机：
//   - 授权回调换到 token 后（首次保存）；
//   - handleOrders 中刷新 token 成功后（覆盖旧值，因为 refresh_token 会变）。
//
// 并发：内部加锁，可被多个请求同时调用。
func (ts *TokenStore) Save(rec TokenRecord) error {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	// 先更新内存索引，再整体落盘。
	ts.data[rec.OwnerID] = rec
	return ts.persistLocked()
}

// Get 按商家 ID 读取一条授权记录。
//
// 参数：
//   - ownerID：商家 ID，即记录的主键。
//
// 返回值：
//   - TokenRecord：找到时的记录副本（值为拷贝，外部修改它不会影响存储内容）；
//   - bool：是否存在该商家。false 表示未授权或记录已被删除。
//
// 调用方据此区分「查无此人（404）」与「查到但需要刷新」两种情况。
func (ts *TokenStore) Get(ownerID string) (TokenRecord, bool) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	r, ok := ts.data[ownerID]
	return r, ok
}

// All 返回全部授权记录的快照。
//
// 返回值：
//   - []TokenRecord：所有商家记录的副本切片。切片本身是新分配的，
//     因此调用方遍历或改动它都不会影响内部 map。
//
// 用途：/tokens 接口展示已授权商家列表。
//
// 注意：返回顺序不确定（map 遍历无序）。若需要稳定顺序，调用方应自行排序。
func (ts *TokenStore) All() []TokenRecord {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	out := make([]TokenRecord, 0, len(ts.data))
	for _, v := range ts.data {
		out = append(out, v)
	}
	return out
}

// persistLocked 把内存中的全部记录序列化后整体写入文件。
//
// 参数：无。它直接使用 ts.data 与 ts.path。
//
// 返回值：
//   - error：序列化失败或写文件失败。
//
// 重要：调用者必须已持有 ts.mu（方法名中的 Locked 即此含义）。
// 本方法内部不再加锁，否则会死锁。
//
// 实现细节：
//   - 用 MarshalIndent 输出带缩进的 JSON，方便人工查看与调试；
//   - 文件权限设为 0600（仅属主可读写），因为文件内含明文 token；
//   - 采用「整体重写」而非追加，简单可靠，但商家数量很多时会有性能开销。
func (ts *TokenStore) persistLocked() error {
	b, err := json.MarshalIndent(ts.data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(ts.path, b, 0o600)
}

// StateStore 是 OAuth state 的内存存储，用于防 CSRF 与防重放。
//
// 设计目的：
//   - 发起授权时生成一个随机 state 并登记；
//   - 回调时校验该 state 是否存在且未过期，校验通过即立刻删除（一次性）。
//     这样即使攻击者截获了回调 URL，也无法二次使用。
//
// 只存内存：进程重启后所有 state 失效，用户重新点一次授权即可，属于可接受的取舍。
// 多实例部署时应改为 Redis 等共享存储。
type StateStore struct {
	// mu 保护 m，保证并发读写安全（回调请求可能并发到达）。
	mu sync.Mutex

	// m 是 state → 过期时刻（Unix 秒）的映射。
	// 存绝对过期时间而不是 TTL 时长，便于每次校验时直接与当前时间比较。
	m map[string]int64
}

// NewStateStore 创建一个空的 state 存储。
//
// 返回值：
//   - *StateStore：初始化完成、map 已分配（非 nil），可直接使用。
//
// 无参数、无副作用。
func NewStateStore() *StateStore {
	return &StateStore{m: map[string]int64{}}
}

// Put 登记一个 state，并为其设置存活时间。
//
// 参数：
//   - state：随机字符串，由 randomState() 生成，需足够随机以防被猜测；
//   - ttl：存活时长，从「现在」开始计算（本程序传入 10 分钟）。
//
// 返回值：无。
//
// 注意：不做去重。若传入已经存在的 state，会直接覆盖其过期时间。
// 由于 state 是 16 字节随机数，实际碰撞概率可忽略。
func (s *StateStore) Put(state string, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 存「绝对过期时刻」，后续 Consume 直接比较时间即可。
	s.m[state] = time.Now().Add(ttl).Unix()
}

// Consume 校验并消费一个 state：存在且未过期才返回 true，并且无论结果如何都会删除它。
//
// 参数：
//   - state：回调 URL 中带回的 state 值。
//
// 返回值：
//   - bool：true 表示校验通过（state 存在且未超时）；false 表示非法、已用过或已过期。
//
// 关键设计（一次性语义）：
//   - 只要该 state 在表中存在，就立即删除，然后才判断是否过期。
//     因此同一个 state 无法被使用第二次，天然防重放；
//   - 删除发生在过期判断之前，意味着「过期的 state 被尝试使用」后也会被清掉，
//     不会残留在内存里变成垃圾。
//
// 为什么先删再判过期，而不是先判再删：
//
//	若先判断过期再删除，那么攻击者可以用一个已过期的 state 反复试探，
//	虽然不会通过校验，但会让该条目长期驻留。先删更干净。
func (s *StateStore) Consume(state string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	exp, ok := s.m[state]
	if !ok {
		// 表中没有：可能从未签发、已被用过一次、或进程重启过。
		return false
	}
	// 先删除，保证一次性。
	delete(s.m, state)

	// 再判断是否过期（<= 表示到点即失效）。
	return time.Now().Unix() <= exp
}
