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

type PddClient struct {
	ClientID     string
	ClientSecret string
	Gateway      string
	HTTP         *http.Client
}

func NewPddClient(clientID, clientSecret, gateway string) *PddClient {
	if gateway == "" {
		gateway = "https://gw-api.pinduoduo.com/api/router"
	}
	return &PddClient{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Gateway:      gateway,
		HTTP:         &http.Client{Timeout: 10 * time.Second},
	}
}

type ErrorResponse struct {
	ErrorCode int64  `json:"error_code"`
	ErrorMsg  string `json:"error_msg"`
}

type Token struct {
	AccessToken  string   `json:"access_token"`
	RefreshToken string   `json:"refresh_token"`
	OwnerID      string   `json:"owner_id"`
	OwnerName    string   `json:"owner_name"`
	Scope        []string `json:"scope"`
	ExpiresIn    int64    `json:"expires_in"`
}

type Order struct {
	OrderSn       string `json:"order_sn"`
	OrderStatus   int    `json:"order_status"`
	GoodsAmount   int64  `json:"goods_amount"`
	ReceiverName  string `json:"receiver_name"`
	ReceiverPhone string `json:"receiver_phone"`
}

// sign 计算拼多多请求签名（MD5，大写）。
// 空值参数是否参与签名请以官方文档为准，本示例默认跳过空值。
func sign(params map[string]string, clientSecret string) string {
	keys := make([]string, 0, len(params))
	for k, v := range params {
		if k == "sign" || v == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var sb strings.Builder
	for _, k := range keys {
		sb.WriteString(k)
		sb.WriteString(params[k])
	}
	sb.WriteString(clientSecret)

	sum := md5.Sum([]byte(sb.String()))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

func (c *PddClient) doRequest(apiType, accessToken string, bizParams map[string]string) (map[string]any, error) {
	params := map[string]string{
		"type":      apiType,
		"client_id": c.ClientID,
		"timestamp": strconv.FormatInt(time.Now().Unix(), 10),
		"data_type": "JSON",
		"version":   "V1",
	}
	if accessToken != "" {
		params["access_token"] = accessToken
	}
	for k, v := range bizParams {
		if v != "" {
			params[k] = v
		}
	}
	params["sign"] = sign(params, c.ClientSecret)

	form := url.Values{}
	for k, v := range params {
		form.Set(k, v)
	}

	resp, err := c.HTTP.PostForm(c.Gateway, form)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("网关返回 HTTP %d: %s", resp.StatusCode, string(body))
	}

	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}

	if er, ok := data["error_response"]; ok && er != nil {
		b, _ := json.Marshal(er)
		return nil, errors.New(string(b))
	}
	return data, nil
}

func (c *PddClient) ExchangeToken(code string) (*Token, error) {
	data, err := c.doRequest("pdd.pop.auth.token.create", "", map[string]string{"code": code})
	if err != nil {
		return nil, err
	}
	return parseToken(data, "pdd_pop_auth_token_create_response")
}

func (c *PddClient) RefreshToken(refreshToken string) (*Token, error) {
	data, err := c.doRequest("pdd.pop.auth.token.refresh", "", map[string]string{"refresh_token": refreshToken})
	if err != nil {
		return nil, err
	}
	return parseToken(data, "pdd_pop_auth_token_refresh_response")
}

func parseToken(data map[string]any, key string) (*Token, error) {
	raw, ok := data[key]
	if !ok || raw == nil {
		return nil, fmt.Errorf("响应中缺少 %s 字段", key)
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var token Token
	if err := json.Unmarshal(b, &token); err != nil {
		return nil, err
	}
	if token.ExpiresIn <= 0 {
		token.ExpiresIn = 86400
	}
	return &token, nil
}

func (c *PddClient) OrderList(accessToken string, page, pageSize int) ([]Order, int, error) {
	data, err := c.doRequest("pdd.order.list.get", accessToken, map[string]string{
		"page":      strconv.Itoa(page),
		"page_size": strconv.Itoa(pageSize),
	})
	if err != nil {
		return nil, 0, err
	}

	raw, ok := data["pdd_order_list_get_response"]
	if !ok || raw == nil {
		return nil, 0, errors.New("响应中缺少 pdd_order_list_get_response 字段")
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, 0, err
	}
	var resp struct {
		OrderList  []Order `json:"order_list"`
		TotalCount int     `json:"total_count"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		return nil, 0, err
	}
	return resp.OrderList, resp.TotalCount, nil
}
