package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	accMu.Lock()
	a := acct
	accMu.Unlock()
	if a == nil {
		writeErr(w, http.StatusInternalServerError, "账户未初始化")
		return
	}
	if req.Username != "" && req.Username != a.Username {
		time.Sleep(250 * time.Millisecond)
		writeErr(w, http.StatusUnauthorized, "用户名或密码错误")
		return
	}
	if !verifyPassword(req.Password, a.PasswordHash) {
		time.Sleep(250 * time.Millisecond)
		writeErr(w, http.StatusUnauthorized, "用户名或密码错误")
		return
	}
	issueSession(w)
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "username": a.Username})
}

func handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "homingmihomo", Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

func handleMe(w http.ResponseWriter, r *http.Request) {
	accMu.Lock()
	name := ""
	if acct != nil {
		name = acct.Username
	}
	accMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]interface{}{"username": name})
}

func (a *App) handleStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	version := ""
	healthy := false
	if v, err := a.client.version(ctx); err == nil {
		version = v
		healthy = true
	}
	up, down := int64(0), int64(0)
	conns := 0
	if c, err := a.client.connections(ctx); err == nil {
		up, down = c.UploadTotal, c.DownloadTotal
		conns = len(c.Connections)
	}
	active, _ := a.store.Active()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"core": map[string]interface{}{
			"running":  a.sup.Running(),
			"healthy":  healthy,
			"version":  version,
			"uptime":   a.sup.Uptime(),
			"lastExit": a.sup.LastExit(),
		},
		"profile":  active,
		"settings": a.Settings(),
		"traffic": map[string]interface{}{
			"connections": conns,
			"upload":      up,
			"download":    down,
		},
		"ports": map[string]interface{}{"proxy": a.Settings().MixedPort, "admin": adminPort},
	})
}

func (a *App) handleProfiles(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list, active := a.store.List()
		writeJSON(w, http.StatusOK, map[string]interface{}{"profiles": list, "active": active})
	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
		var req struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		p, err := a.store.AddURL(req.Name, req.URL)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "profile": p})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleProfileAction(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/profiles/")
	rest = trimSlash(rest)
	if rest == "" {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if rest == "upload" {
		if r.Method != http.MethodPost {
			writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 40<<20)
		if err := r.ParseMultipartForm(40 << 20); err != nil {
			writeErr(w, http.StatusBadRequest, "文件解析失败: "+err.Error())
			return
		}
		f, _, err := r.FormFile("file")
		if err != nil {
			writeErr(w, http.StatusBadRequest, "缺少 file 文件")
			return
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, (32<<20)+1))
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if len(data) > 32<<20 {
			writeErr(w, http.StatusBadRequest, "文件超过 32MB")
			return
		}
		p, err := a.store.AddYAML(r.FormValue("name"), "file", "", data)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "profile": p})
		return
	}
	if rest == "refresh" {
		if r.Method != http.MethodPost {
			writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		list, active := a.store.List()
		var errs []string
		for _, p := range list {
			if p.Kind != "url" {
				continue
			}
			old, _ := a.store.Read(p.ID)
			if _, err := a.store.Update(p.ID); err != nil {
				errs = append(errs, p.Name+": "+err.Error())
				continue
			}
			if p.ID == active {
				if err := a.ActivateProfile(p.ID); err != nil {
					_ = a.store.WriteRaw(p.ID, old)
					errs = append(errs, p.Name+": "+err.Error())
				}
			}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": len(errs) == 0, "errors": errs})
		return
	}

	parts := strings.SplitN(rest, "/", 2)
	id := parts[0]
	action := ""
	if len(parts) == 2 {
		action = parts[1]
	}
	p, ok := a.store.Get(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "订阅不存在")
		return
	}
	switch action {
	case "activate":
		if r.Method != http.MethodPost {
			writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if err := a.ActivateProfile(id); err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
	case "update":
		if r.Method != http.MethodPost {
			writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		old, _ := a.store.Read(id)
		updated, err := a.store.Update(id)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		_, active := a.store.List()
		if active == id {
			if err := a.ActivateProfile(id); err != nil {
				_ = a.store.WriteRaw(id, old)
				writeErr(w, http.StatusBadGateway, err.Error())
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "profile": updated})
	case "delete":
		if r.Method != http.MethodDelete {
			writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if err := a.store.Delete(id); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
	default:
		_ = p
		writeErr(w, http.StatusNotFound, "not found")
	}
}

type proxyNode struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type proxyGroup struct {
	Name  string      `json:"name"`
	Type  string      `json:"type"`
	Now   string      `json:"now"`
	Nodes []proxyNode `json:"nodes"`
}

func (a *App) handleProxies(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	proxies, err := a.client.proxies(ctx)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	groups := make([]proxyGroup, 0)
	for name, p := range proxies {
		if len(p.All) == 0 {
			continue
		}
		g := proxyGroup{Name: name, Type: p.Type, Now: p.Now}
		for _, n := range p.All {
			np := proxies[n]
			g.Nodes = append(g.Nodes, proxyNode{Name: n, Type: np.Type})
		}
		groups = append(groups, g)
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Name == "GLOBAL" {
			return true
		}
		if groups[j].Name == "GLOBAL" {
			return false
		}
		return groups[i].Name < groups[j].Name
	})
	writeJSON(w, http.StatusOK, map[string]interface{}{"groups": groups})
}

func (a *App) handleSelectProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Group string `json:"group"`
		Name  string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if strings.TrimSpace(req.Group) == "" || strings.TrimSpace(req.Name) == "" {
		writeErr(w, http.StatusBadRequest, "group/name 不能为空")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	if err := a.client.selectProxy(ctx, req.Group, req.Name); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

func (a *App) handleTestProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Names []string `json:"names"`
		URL   string   `json:"url"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	testURL := strings.TrimSpace(req.URL)
	if testURL == "" {
		testURL = "http://www.gstatic.com/generate_204"
	}
	u, err := url.Parse(testURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		writeErr(w, http.StatusBadRequest, "测速 URL 必须是 http/https")
		return
	}
	if len(req.Names) > 500 {
		writeErr(w, http.StatusBadRequest, "一次最多测试 500 个节点")
		return
	}
	results := map[string]interface{}{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, name := range req.Names {
		name := name
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			defer cancel()
			delay, err := a.client.delay(ctx, name, testURL, 5000)
			mu.Lock()
			if err != nil {
				results[name] = map[string]interface{}{"delay": 0, "error": err.Error()}
			} else {
				results[name] = map[string]interface{}{"delay": delay}
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, map[string]interface{}{"results": results})
}

func (a *App) handleRules(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	rules, err := a.client.rules(ctx)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"rules": rules})
}

func (a *App) handleConnections(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	switch r.Method {
	case http.MethodGet:
		c, err := a.client.connections(ctx)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, c)
	case http.MethodDelete:
		if err := a.client.closeAllConnections(ctx); err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleConnectionAction(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/connections/")
	if id == "" {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodDelete {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	if err := a.client.closeConnection(ctx, id); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

func (a *App) handleLogs(w http.ResponseWriter, r *http.Request) {
	after := int64(0)
	if v := r.URL.Query().Get("after"); v != "" {
		_, _ = fmt.Sscanf(v, "%d", &after)
	}
	lines, next := a.sup.ring.Since(after)
	writeJSON(w, http.StatusOK, map[string]interface{}{"lines": lines, "next": next})
}

func (a *App) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]interface{}{"settings": a.Settings()})
	case http.MethodPut:
		var s Settings
		if err := json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&s); err != nil {
			writeErr(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		s.DNSServers = normalizeCSV(s.DNSServers)
		s.DNSListen = strings.TrimSpace(s.DNSListen)
		s.TunDevice = strings.TrimSpace(s.TunDevice)
		s.TunStack = strings.TrimSpace(s.TunStack)
		s.Mode = strings.TrimSpace(s.Mode)
		s.DNSMode = strings.TrimSpace(s.DNSMode)
		s.ProxyAuthUser = strings.TrimSpace(s.ProxyAuthUser)
		if err := validateSettings(s); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := a.ApplySettings(s); err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "settings": a.Settings()})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if err := a.RestartCore(); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}
