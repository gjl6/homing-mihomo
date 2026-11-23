package main

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const testProfile = `
proxies:
  - name: "node-a"
    type: socks5
    server: 192.0.2.10
    port: 1080
proxy-groups:
  - name: "PROXY"
    type: select
    proxies: ["node-a", "DIRECT"]
rules:
  - "DOMAIN-SUFFIX,example.com,PROXY"
  - "MATCH,DIRECT"
`

func TestRenderOverridesAndPreserves(t *testing.T) {
	s := defaultSettings()
	s.MixedPort = 7899
	s.AllowLan = true
	s.Mode = "global"
	s.IPv6 = true
	s.TunEnabled = false
	s.DNSMode = "off"
	out, err := renderConfig([]byte(testProfile), s, "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	for _, want := range []string{
		`mixed-port: 7899`,
		`allow-lan: true`,
		`mode: global`,
		`ipv6: true`,
		`external-controller: 127.0.0.1:9091`,
		`secret: test-secret`,
		`bind-address: '*'`,
		`enable: false`,
		`node-a`,
		`DOMAIN-SUFFIX,example.com,PROXY`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("rendered config missing %q:\n%s", want, text)
		}
	}
	if got := parseString(t, out, "tun", "enable"); got != "false" {
		t.Fatalf("tun.enable = %q, want false", got)
	}
}

func TestRenderTunAndCustomDNS(t *testing.T) {
	s := defaultSettings()
	s.TunEnabled = true
	s.TunDevice = "homing"
	s.TunAutoRoute = false
	s.DNSMode = "custom"
	s.DNSListen = "0.0.0.0:1053"
	s.DNSServers = "1.1.1.1,8.8.8.8"
	s.FakeIP = true
	out, err := renderConfig([]byte(testProfile), s, "s")
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	for _, want := range []string{
		`enable: true`,
		`device: homing`,
		`auto-route: false`,
		`dns-hijack:`,
		`listen: 0.0.0.0:1053`,
		`enhanced-mode: fake-ip`,
		`1.1.1.1`,
		`8.8.8.8`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("rendered config missing %q:\n%s", want, text)
		}
	}
}

func TestRenderOverride(t *testing.T) {
	s := defaultSettings()
	s.OverrideYAML = `
proxies:
  - name: extra
    type: socks5
    server: 192.0.2.20
    port: 1080
rules:
  - MATCH,extra
`
	out, err := renderConfig([]byte(testProfile), s, "s")
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	if !strings.Contains(text, "extra") || !strings.Contains(text, "MATCH,extra") {
		t.Fatalf("override not merged:\n%s", text)
	}
	// 系统字段仍应被控制台覆盖
	if !strings.Contains(text, "mixed-port: 7890") {
		t.Fatalf("system fields not enforced:\n%s", text)
	}
}

func TestRenderProxyAuthentication(t *testing.T) {
	s := defaultSettings()
	s.ProxyAuthUser = "user"
	s.ProxyAuthPass = "pass"
	out, err := renderConfig([]byte(testProfile), s, "s")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `authentication:`) || !strings.Contains(string(out), `user:pass`) {
		t.Fatalf("authentication not rendered:\n%s", out)
	}
}

func TestRenderRejectsNonMapping(t *testing.T) {
	if _, err := renderConfig([]byte("- a\n- b\n"), defaultSettings(), "s"); err == nil {
		t.Fatal("list profile should be rejected")
	}
}

func TestValidateSettings(t *testing.T) {
	s := defaultSettings()
	if err := validateSettings(s); err != nil {
		t.Fatalf("default settings rejected: %v", err)
	}
	s.MixedPort = adminPort
	if err := validateSettings(s); err == nil {
		t.Fatal("mixed port equal admin port should be rejected")
	}
	s = defaultSettings()
	s.DNSMode = "custom"
	s.DNSListen = "bad"
	if err := validateSettings(s); err == nil {
		t.Fatal("bad dns listen should be rejected")
	}
}

func parseString(t *testing.T, out []byte, keys ...string) string {
	t.Helper()
	var m map[string]interface{}
	if err := yaml.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	var cur interface{} = m
	for _, k := range keys {
		mm, ok := cur.(map[string]interface{})
		if !ok {
			return ""
		}
		cur = mm[k]
	}
	switch v := cur.(type) {
	case bool:
		if v {
			return "true"
		}
		return "false"
	case string:
		return v
	}
	return ""
}
