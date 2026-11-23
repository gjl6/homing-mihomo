package config

import (
	"bytes"
	"fmt"
	"net"
	"strings"

	"gopkg.in/yaml.v3"
)

// ParseProfile 只接受 YAML mapping，拒绝空文件/纯列表。
func ParseProfile(b []byte) (map[string]interface{}, error) {
	var m map[string]interface{}
	if err := yaml.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("YAML 解析失败: %w", err)
	}
	if len(m) == 0 {
		return nil, fmt.Errorf("配置为空或不是 YAML mapping")
	}
	return m, nil
}

func parseOverrideYAML(s string) (map[string]interface{}, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var m map[string]interface{}
	if err := yaml.Unmarshal([]byte(s), &m); err != nil {
		return nil, fmt.Errorf("解析失败: %w", err)
	}
	return m, nil
}

func mergeMaps(dst, src map[string]interface{}) {
	for k, v := range src {
		sm, ok := v.(map[string]interface{})
		if !ok {
			dst[k] = v
			continue
		}
		dm, ok := dst[k].(map[string]interface{})
		if !ok {
			dst[k] = sm
			continue
		}
		mergeMaps(dm, sm)
	}
}

// RenderConfig 把订阅配置 + 控制台覆盖项生成最终运行配置。
// 保留订阅的 proxies / proxy-groups / rules / providers 等，只覆盖入口、API、TUN、DNS。
func RenderConfig(profile []byte, s Settings, secret string) ([]byte, error) {
	m, err := ParseProfile(profile)
	if err != nil {
		return nil, err
	}
	if ov, err := parseOverrideYAML(s.OverrideYAML); err != nil {
		return nil, err
	} else if len(ov) > 0 {
		mergeMaps(m, ov)
	}

	// 控制台只开放一个 mixed 入口，避免订阅自带端口和部署端口冲突。
	m["mixed-port"] = s.MixedPort
	m["port"] = 0
	m["socks-port"] = 0
	m["redir-port"] = 0
	m["tproxy-port"] = 0
	m["allow-lan"] = s.AllowLan
	m["bind-address"] = "*"
	m["mode"] = s.Mode
	m["ipv6"] = s.IPv6
	m["log-level"] = "info"
	m["external-controller"] = fmt.Sprintf("127.0.0.1:%d", MihomoAPIPort)
	m["secret"] = secret
	if s.ProxyAuthUser != "" {
		m["authentication"] = []string{s.ProxyAuthUser + ":" + s.ProxyAuthPass}
	} else {
		delete(m, "authentication")
	}

	// TUN 默认关闭；关闭时也显式写 enable:false，压掉订阅里的 TUN。
	tun := map[string]interface{}{
		"enable":                s.TunEnabled,
		"device":                s.TunDevice,
		"stack":                 s.TunStack,
		"auto-route":            s.TunAutoRoute,
		"auto-redirect":         false,
		"auto-detect-interface": false,
		"mtu":                   1500,
	}
	if s.TunEnabled && s.DNSMode == "custom" {
		tun["dns-hijack"] = []interface{}{"any:53"}
	}
	m["tun"] = tun

	switch s.DNSMode {
	case "off":
		m["dns"] = map[string]interface{}{"enable": false}
	case "custom":
		servers := SplitCSV(s.DNSServers)
		dns := map[string]interface{}{
			"enable":             true,
			"listen":             s.DNSListen,
			"ipv6":               false,
			"nameserver":         servers,
			"default-nameserver": servers,
		}
		if s.FakeIP {
			dns["enhanced-mode"] = "fake-ip"
			dns["fake-ip-range"] = "198.18.0.1/16"
			dns["fake-ip-filter"] = []interface{}{
				"*.lan", "*.local", "+.local", "+.home.arpa",
				"localhost.ptlogin2.qq.com", "+.msftconnecttest.com", "+.msftncsi.com",
			}
		} else {
			dns["enhanced-mode"] = "redir-host"
		}
		m["dns"] = dns
	case "follow":
		// 保留订阅自己的 DNS 配置，但把监听地址收紧到本机，避免在 host 网络下
		// 意外把 DNS 解析器暴露给整个局域网。
		if raw, ok := m["dns"]; ok {
			if dm, ok := raw.(map[string]interface{}); ok {
				if listen, ok := dm["listen"].(string); ok && listen != "" {
					host, port, err := net.SplitHostPort(listen)
					if err == nil && (host == "" || host == "0.0.0.0" || host == "::") {
						dm["listen"] = "127.0.0.1:" + port
					}
				}
			}
		}
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(m); err != nil {
		return nil, fmt.Errorf("生成运行配置失败: %w", err)
	}
	_ = enc.Close()
	return buf.Bytes(), nil
}

// DefaultProfileYAML 返回内置的默认直连配置。
func DefaultProfileYAML() []byte {
	return []byte(`# homing-mihomo 默认直连配置
mixed-port: 7890
allow-lan: false
mode: rule
log-level: info
proxies: []
proxy-groups:
  - name: PROXY
    type: select
    proxies:
      - DIRECT
rules:
  - MATCH,DIRECT
`)
}
