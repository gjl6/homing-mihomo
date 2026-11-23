// Package kernel 封装 mihomo external-controller 的 HTTP 客户端与内核进程守护。
package kernel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client 是 127.0.0.1 external-controller 的 HTTP 客户端。
type Client struct {
	base   string
	secret string
	http   *http.Client
}

// NewClient 创建内核 API 客户端；apiPort 是 external-controller 的本地端口。
func NewClient(apiPort int, secret string) *Client {
	return &Client{
		base:   fmt.Sprintf("http://127.0.0.1:%d", apiPort),
		secret: secret,
		http:   &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *Client) do(ctx context.Context, method, path string, body interface{}) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.secret)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = resp.Status
		}
		return nil, fmt.Errorf("mihomo API %s %s: %s", method, path, msg)
	}
	return out, nil
}

// Version 返回内核版本。
func (c *Client) Version(ctx context.Context) (string, error) {
	out, err := c.do(ctx, http.MethodGet, "/version", nil)
	if err != nil {
		return "", err
	}
	var v struct {
		Version string `json:"version"`
		Meta    bool   `json:"meta"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return "", err
	}
	if v.Version == "" {
		return "unknown", nil
	}
	return v.Version, nil
}

// Proxy 是 /proxies 返回的单个代理或策略组。
type Proxy struct {
	Type string   `json:"type"`
	Now  string   `json:"now"`
	All  []string `json:"all"`
	UDP  bool     `json:"udp"`
}

// Proxies 返回内核当前的代理/策略组列表。
func (c *Client) Proxies(ctx context.Context) (map[string]Proxy, error) {
	out, err := c.do(ctx, http.MethodGet, "/proxies", nil)
	if err != nil {
		return nil, err
	}
	var v struct {
		Proxies map[string]Proxy `json:"proxies"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return nil, err
	}
	return v.Proxies, nil
}

// SelectProxy 在策略组中选择指定节点。
func (c *Client) SelectProxy(ctx context.Context, group, name string) error {
	_, err := c.do(ctx, http.MethodPut, "/proxies/"+url.PathEscape(group), map[string]string{"name": name})
	return err
}

// Delay 对单个节点做延迟测速，返回毫秒延迟。
func (c *Client) Delay(ctx context.Context, name, testURL string, timeoutMS int) (int, error) {
	path := fmt.Sprintf("/proxies/%s/delay?timeout=%d&url=%s",
		url.PathEscape(name), timeoutMS, url.QueryEscape(testURL))
	out, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return 0, err
	}
	var v struct {
		Delay int `json:"delay"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return 0, err
	}
	if v.Delay <= 0 {
		return 0, fmt.Errorf("不可用")
	}
	return v.Delay, nil
}

// Rule 是 /rules 返回的单条规则。
type Rule struct {
	Type    string `json:"type"`
	Payload string `json:"payload"`
	Proxy   string `json:"proxy"`
}

// Rules 返回内核当前生效的规则列表。
func (c *Client) Rules(ctx context.Context) ([]Rule, error) {
	out, err := c.do(ctx, http.MethodGet, "/rules", nil)
	if err != nil {
		return nil, err
	}
	var v struct {
		Rules []Rule `json:"rules"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return nil, err
	}
	return v.Rules, nil
}

// Connections 是 /connections 的响应。
type Connections struct {
	DownloadTotal int64                    `json:"downloadTotal"`
	UploadTotal   int64                    `json:"uploadTotal"`
	Connections   []map[string]interface{} `json:"connections"`
}

// Connections 返回当前连接与累计流量。
func (c *Client) Connections(ctx context.Context) (*Connections, error) {
	out, err := c.do(ctx, http.MethodGet, "/connections", nil)
	if err != nil {
		return nil, err
	}
	var v Connections
	if err := json.Unmarshal(out, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// CloseConnection 关闭单条连接。
func (c *Client) CloseConnection(ctx context.Context, id string) error {
	_, err := c.do(ctx, http.MethodDelete, "/connections/"+url.PathEscape(id), nil)
	return err
}

// CloseAllConnections 关闭全部连接。
func (c *Client) CloseAllConnections(ctx context.Context) error {
	_, err := c.do(ctx, http.MethodDelete, "/connections", nil)
	return err
}

// Healthy 通过查询版本判断内核是否已就绪。
func (c *Client) Healthy(ctx context.Context) bool {
	_, err := c.Version(ctx)
	return err == nil
}
