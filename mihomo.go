package main

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

type mihomoClient struct {
	base   string
	secret string
	http   *http.Client
}

func newMihomoClient(secret string) *mihomoClient {
	return &mihomoClient{
		base:   fmt.Sprintf("http://127.0.0.1:%d", mihomoAPIPort),
		secret: secret,
		http:   &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *mihomoClient) do(ctx context.Context, method, path string, body interface{}) ([]byte, error) {
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

func (c *mihomoClient) version(ctx context.Context) (string, error) {
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

type mihomoProxy struct {
	Type string   `json:"type"`
	Now  string   `json:"now"`
	All  []string `json:"all"`
	UDP  bool     `json:"udp"`
}

func (c *mihomoClient) proxies(ctx context.Context) (map[string]mihomoProxy, error) {
	out, err := c.do(ctx, http.MethodGet, "/proxies", nil)
	if err != nil {
		return nil, err
	}
	var v struct {
		Proxies map[string]mihomoProxy `json:"proxies"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return nil, err
	}
	return v.Proxies, nil
}

func (c *mihomoClient) selectProxy(ctx context.Context, group, name string) error {
	_, err := c.do(ctx, http.MethodPut, "/proxies/"+url.PathEscape(group), map[string]string{"name": name})
	return err
}

func (c *mihomoClient) delay(ctx context.Context, name, testURL string, timeoutMS int) (int, error) {
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

type mihomoRule struct {
	Type    string `json:"type"`
	Payload string `json:"payload"`
	Proxy   string `json:"proxy"`
}

func (c *mihomoClient) rules(ctx context.Context) ([]mihomoRule, error) {
	out, err := c.do(ctx, http.MethodGet, "/rules", nil)
	if err != nil {
		return nil, err
	}
	var v struct {
		Rules []mihomoRule `json:"rules"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return nil, err
	}
	return v.Rules, nil
}

type mihomoConnections struct {
	DownloadTotal int64                    `json:"downloadTotal"`
	UploadTotal   int64                    `json:"uploadTotal"`
	Connections   []map[string]interface{} `json:"connections"`
}

func (c *mihomoClient) connections(ctx context.Context) (*mihomoConnections, error) {
	out, err := c.do(ctx, http.MethodGet, "/connections", nil)
	if err != nil {
		return nil, err
	}
	var v mihomoConnections
	if err := json.Unmarshal(out, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

func (c *mihomoClient) closeConnection(ctx context.Context, id string) error {
	_, err := c.do(ctx, http.MethodDelete, "/connections/"+url.PathEscape(id), nil)
	return err
}

func (c *mihomoClient) closeAllConnections(ctx context.Context) error {
	_, err := c.do(ctx, http.MethodDelete, "/connections", nil)
	return err
}

func (c *mihomoClient) healthy(ctx context.Context) bool {
	_, err := c.version(ctx)
	return err == nil
}
