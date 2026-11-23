// Package config 负责控制台设置、订阅配置渲染以及通用的文件工具。
package config

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

// MihomoAPIPort 是内核 external-controller 在本机监听的端口。
const MihomoAPIPort = 9091

// Settings 是控制台自己的覆盖项；订阅里的其它配置原样保留。
type Settings struct {
	MixedPort     int    `json:"mixedPort"`
	AllowLan      bool   `json:"allowLan"`
	ProxyAuthUser string `json:"proxyAuthUser"`
	ProxyAuthPass string `json:"proxyAuthPass"`
	Mode          string `json:"mode"` // rule / global / direct
	IPv6          bool   `json:"ipv6"`
	TunEnabled    bool   `json:"tunEnabled"`
	TunDevice     string `json:"tunDevice"`
	TunStack      string `json:"tunStack"`
	TunAutoRoute  bool   `json:"tunAutoRoute"`
	DNSMode       string `json:"dnsMode"` // follow / custom / off
	DNSListen     string `json:"dnsListen"`
	DNSServers    string `json:"dnsServers"`
	FakeIP        bool   `json:"fakeIp"`
	// OverrideYAML 深度合并到订阅配置之上；入口/API/TUN/DNS 等控制台字段仍强制覆盖。
	OverrideYAML string `json:"overrideYaml"`
}

// DefaultSettings 返回控制台默认设置。
func DefaultSettings() Settings {
	return Settings{
		MixedPort:    7890,
		AllowLan:     true,
		Mode:         "rule",
		IPv6:         false,
		TunEnabled:   false,
		TunDevice:    "mihomo",
		TunStack:     "gvisor",
		TunAutoRoute: true,
		DNSMode:      "follow",
		DNSListen:    "0.0.0.0:1053",
		DNSServers:   "223.5.5.5,119.29.29.29",
		FakeIP:       true,
	}
}

// LoadSettings 读取设置文件；文件不存在或字段缺失时回退默认值。
func LoadSettings(path string) Settings {
	s := DefaultSettings()
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	if s.MixedPort == 0 {
		s.MixedPort = 7890
	}
	if s.Mode == "" {
		s.Mode = "rule"
	}
	if s.TunDevice == "" {
		s.TunDevice = "mihomo"
	}
	if s.TunStack == "" {
		s.TunStack = "gvisor"
	}
	if s.DNSMode == "" {
		s.DNSMode = "follow"
	}
	if s.DNSListen == "" {
		s.DNSListen = "0.0.0.0:1053"
	}
	if s.DNSServers == "" {
		s.DNSServers = "223.5.5.5,119.29.29.29"
	}
	return s
}

// SaveSettings 原子写入设置文件。
func SaveSettings(path string, s Settings) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(path, b, 0o600)
}

// ValidateSettings 校验控制台设置；adminPort 用于检查端口冲突。
func ValidateSettings(s Settings, adminPort int) error {
	if s.MixedPort < 1 || s.MixedPort > 65535 {
		return fmt.Errorf("代理端口必须在 1~65535")
	}
	if s.MixedPort == adminPort {
		return fmt.Errorf("代理端口不能与 Web 管理端口 %d 相同", adminPort)
	}
	if s.MixedPort == MihomoAPIPort {
		return fmt.Errorf("代理端口不能与内核 API 端口 %d 相同", MihomoAPIPort)
	}
	if (s.ProxyAuthUser == "") != (s.ProxyAuthPass == "") {
		return fmt.Errorf("代理认证用户名和密码必须同时填写或同时留空")
	}
	if strings.ContainsAny(s.ProxyAuthUser, ":\r\n") {
		return fmt.Errorf("代理认证用户名不能包含冒号或换行")
	}
	if strings.ContainsAny(s.ProxyAuthPass, "\r\n") {
		return fmt.Errorf("代理认证密码不能包含换行")
	}
	if len(s.ProxyAuthUser) > 128 || len(s.ProxyAuthPass) > 256 {
		return fmt.Errorf("代理认证用户名/密码过长")
	}
	switch s.Mode {
	case "rule", "global", "direct":
	default:
		return fmt.Errorf("模式必须是 rule / global / direct")
	}
	if s.TunEnabled {
		if err := validateIfaceName(s.TunDevice); err != nil {
			return fmt.Errorf("TUN 设备: %w", err)
		}
		switch s.TunStack {
		case "gvisor", "system", "mixed", "mips":
		default:
			return fmt.Errorf("TUN 协议栈必须是 gvisor / system / mixed / mips")
		}
		if _, err := os.Stat("/dev/net/tun"); err != nil {
			return fmt.Errorf("当前容器没有 /dev/net/tun，无法开启 TUN；请用 host 网络 + NET_ADMIN + /dev/net/tun 重新部署")
		}
	}
	if len(s.OverrideYAML) > 512<<10 {
		return fmt.Errorf("配置覆写超过 512KB")
	}
	if _, err := parseOverrideYAML(s.OverrideYAML); err != nil {
		return fmt.Errorf("配置覆写 YAML: %w", err)
	}
	switch s.DNSMode {
	case "follow":
	case "off":
	case "custom":
		if _, err := listenPort(s.DNSListen); err != nil {
			return fmt.Errorf("DNS 监听: %w", err)
		}
		if len(SplitCSV(s.DNSServers)) == 0 {
			return fmt.Errorf("自定义 DNS 模式需要填写 DNS 上游")
		}
		for _, part := range SplitCSV(s.DNSServers) {
			if err := validateDNSServer(part); err != nil {
				return fmt.Errorf("DNS 上游 %q: %w", part, err)
			}
		}
	default:
		return fmt.Errorf("DNS 模式必须是 follow / custom / off")
	}
	return nil
}

func validateIfaceName(s string) error {
	if len(s) == 0 || len(s) > 15 {
		return fmt.Errorf("设备名长度必须在 1~15 个字符")
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '.' || r == '-') {
			return fmt.Errorf("设备名只能包含字母、数字、_ . -")
		}
	}
	return nil
}

func listenPort(addr string) (int, error) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return 0, fmt.Errorf("格式应为 IP:端口，如 0.0.0.0:1053")
	}
	if host != "" && net.ParseIP(strings.Trim(host, "[]")) == nil {
		return 0, fmt.Errorf("监听地址必须是 IP")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("端口必须在 1~65535")
	}
	return n, nil
}

func validateDNSServer(s string) error {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 253 {
		return fmt.Errorf("格式不正确")
	}
	host := s
	if h, p, err := net.SplitHostPort(s); err == nil {
		host = h
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("端口必须在 1~65535")
		}
	}
	host = strings.Trim(host, "[]")
	if net.ParseIP(host) == nil {
		return fmt.Errorf("只支持 IP（可带端口）")
	}
	return nil
}

// SplitCSV 把逗号分隔的列表拆成去空白的字符串切片。
func SplitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// NormalizeCSV 规范化逗号分隔的列表。
func NormalizeCSV(s string) string {
	return strings.Join(SplitCSV(s), ",")
}
