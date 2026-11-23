// homing-mihomo：mihomo 内核 + 自研管理台的一体化镜像。
//
// 一个进程负责：
//   - 生成 / 校验 mihomo 运行配置；
//   - 拉起并守护 mihomo；
//   - 提供登录后的管理页面和 API；
//   - 通过 127.0.0.1 的 external-controller 控制内核。
package main

import (
	"context"
	"embed"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

//go:embed ui.html
var uiFS embed.FS

const (
	mihomoAPIPort = 9091
	defaultHTTP   = 8088
)

var (
	dataDir       = getenv("DATA_DIR", "/data")
	listenAddr    = getenv("ADMIN_LISTEN", ":8088")
	adminUser     = getenv("ADMIN_USER", "admin")
	adminPassword = os.Getenv("ADMIN_PASSWORD")
	binPath       = getenv("MIHOMO_BIN", "/usr/local/bin/mihomo")
	geodataDir    = getenv("GEODATA_DIR", "/opt/mihomo-geodata")
	adminPort     = detectPort(listenAddr, defaultHTTP)

	settingsPath      = filepath.Join(dataDir, "settings.json")
	runtimeConfigPath = filepath.Join(dataDir, "runtime", "config.yaml")
	secretPath        = filepath.Join(dataDir, "mihomo-secret")
	secret            string
)

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func detectPort(addr string, def int) int {
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return def
	}
	n, err := strconv.Atoi(p)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// seedGeodata 把镜像内置的 geoip/geosite 放到 mihomo 工作目录。
// 国内/受限网络下 mihomo 首次校验会尝试从 GitHub 下载 MMDB，经常超时；
// 内置数据可以让订阅里的 GEOIP/GEOSITE 规则直接可用。
func seedGeodata() {
	for _, name := range []string{"geoip.metadb", "geosite.dat"} {
		src := filepath.Join(geodataDir, name)
		sb, err := os.Stat(src)
		if err != nil || sb.Size() == 0 {
			continue
		}
		dst := filepath.Join(dataDir, "mihomo", name)
		if db, err := os.Stat(dst); err == nil && db.Size() >= 1<<20 {
			continue
		}
		b, err := os.ReadFile(src)
		if err != nil {
			log.Printf("读取内置 geodata %s 失败: %v", name, err)
			continue
		}
		if err := writeFileAtomic(dst, b, 0o644); err != nil {
			log.Printf("写入 %s 失败: %v", name, err)
			continue
		}
		log.Printf("已内置 geodata: %s (%d bytes)", name, len(b))
	}
}

func loadOrCreateSecret() (string, error) {
	if s := readFileString(secretPath); s != "" {
		return s, nil
	}
	s := randomHex(24)
	if err := writeFileAtomic(secretPath, []byte(s+"\n"), 0o600); err != nil {
		return "", err
	}
	return s, nil
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	for _, d := range []string{
		dataDir,
		filepath.Join(dataDir, "runtime"),
		filepath.Join(dataDir, "backups"),
		filepath.Join(dataDir, "mihomo"),
	} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			log.Fatalf("创建目录 %s 失败: %v", d, err)
		}
	}
	seedGeodata()
	if err := loadAccount(); err != nil {
		log.Fatal(err)
	}
	var err error
	secret, err = loadOrCreateSecret()
	if err != nil {
		log.Fatalf("初始化内核 secret 失败: %v", err)
	}

	settings := loadSettings()
	store, err := newProfileStore(dataDir)
	if err != nil {
		log.Fatalf("初始化订阅存储失败: %v", err)
	}
	active, ok := store.Active()
	if !ok {
		log.Fatal("没有可用订阅")
	}
	profile, err := store.Read(active.ID)
	if err != nil {
		profile = defaultProfileYAML()
	}
	cfg, err := renderConfig(profile, settings, secret)
	if err != nil {
		log.Printf("当前订阅生成配置失败（%v），回退默认直连配置", err)
		cfg, _ = renderConfig(defaultProfileYAML(), settings, secret)
	}
	if err := writeFileAtomic(runtimeConfigPath, cfg, 0o600); err != nil {
		log.Fatalf("写入运行配置失败: %v", err)
	}

	ring := newLogRing(800)
	client := newMihomoClient(secret)
	sup := newSupervisor(binPath, dataDir, runtimeConfigPath, ring, client)
	app := newApp(dataDir, runtimeConfigPath, binPath, sup, client, store, settings)

	sup.Start()
	go func() {
		if !sup.WaitHealthy(10 * time.Second) {
			log.Printf("mihomo 尚未就绪，管理台仍会启动；请检查日志或修正订阅")
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/api/login", handleLogin)
	mux.HandleFunc("/api/logout", handleLogout)
	mux.HandleFunc("/api/me", requireAuth(handleMe))
	mux.HandleFunc("/api/status", requireAuth(app.handleStatus))
	mux.HandleFunc("/api/profiles", requireAuth(app.handleProfiles))
	mux.HandleFunc("/api/profiles/", requireAuth(app.handleProfileAction))
	mux.HandleFunc("/api/proxies", requireAuth(app.handleProxies))
	mux.HandleFunc("/api/proxies/select", requireAuth(app.handleSelectProxy))
	mux.HandleFunc("/api/proxies/test", requireAuth(app.handleTestProxy))
	mux.HandleFunc("/api/rules", requireAuth(app.handleRules))
	mux.HandleFunc("/api/connections", requireAuth(app.handleConnections))
	mux.HandleFunc("/api/connections/", requireAuth(app.handleConnectionAction))
	mux.HandleFunc("/api/logs", requireAuth(app.handleLogs))
	mux.HandleFunc("/api/settings", requireAuth(app.handleSettings))
	mux.HandleFunc("/api/core/restart", requireAuth(app.handleRestart))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		b, _ := uiFS.ReadFile("ui.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
		_, _ = w.Write(b)
	})

	srv := &http.Server{Addr: listenAddr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		log.Printf("homing-mihomo 管理台监听 %s（代理端口见设置）", listenAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	sup.Stop()
}

func trimSlash(s string) string {
	return strings.Trim(s, "/")
}
