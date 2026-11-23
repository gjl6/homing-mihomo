// homing-mihomo：mihomo 内核 + 自研管理台的一体化镜像。
//
// 一个进程负责：
//   - 生成 / 校验 mihomo 运行配置；
//   - 拉起并守护 mihomo；
//   - 提供登录后的管理页面和 API；
//   - 通过 127.0.0.1 的 external-controller 控制内核。
//
// 本文件只做进程装配：环境变量、目录、secret、设置、订阅存储、配置渲染、
// 内核守护、HTTP 路由与信号处理；具体职责在 internal/ 与 web/ 中实现。
package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"homing-mihomo/internal/config"
	"homing-mihomo/internal/console"
	"homing-mihomo/internal/kernel"
	"homing-mihomo/internal/profiles"
	"homing-mihomo/web"
)

const defaultHTTP = 8088

var (
	dataDir       = getenv("DATA_DIR", "/data")
	listenAddr    = getenv("ADMIN_LISTEN", ":8088")
	adminUser     = getenv("ADMIN_USER", "admin")
	adminPassword = os.Getenv("ADMIN_PASSWORD")
	binPath       = getenv("MIHOMO_BIN", "/usr/local/bin/mihomo")
	geodataDir    = getenv("GEODATA_DIR", "/opt/mihomo-geodata")
	adminPort     = detectPort(listenAddr, defaultHTTP)

	settingsPath      = filepath.Join(dataDir, "settings.json")
	accountPath       = filepath.Join(dataDir, "admin.json")
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
		if err := config.WriteFileAtomic(dst, b, 0o644); err != nil {
			log.Printf("写入 %s 失败: %v", name, err)
			continue
		}
		log.Printf("已内置 geodata: %s (%d bytes)", name, len(b))
	}
}

func loadOrCreateSecret() (string, error) {
	if s := config.ReadFileString(secretPath); s != "" {
		return s, nil
	}
	s := config.RandomHex(24)
	if err := config.WriteFileAtomic(secretPath, []byte(s+"\n"), 0o600); err != nil {
		return "", err
	}
	return s, nil
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
	auth, err := console.LoadAuth(accountPath, adminUser, adminPassword)
	if err != nil {
		log.Fatal(err)
	}
	secret, err = loadOrCreateSecret()
	if err != nil {
		log.Fatalf("初始化内核 secret 失败: %v", err)
	}

	settings := config.LoadSettings(settingsPath)
	store, err := profiles.NewStore(dataDir)
	if err != nil {
		log.Fatalf("初始化订阅存储失败: %v", err)
	}
	active, ok := store.Active()
	if !ok {
		log.Fatal("没有可用订阅")
	}
	profile, err := store.Read(active.ID)
	if err != nil {
		profile = config.DefaultProfileYAML()
	}
	cfg, err := config.RenderConfig(profile, settings, secret)
	if err != nil {
		log.Printf("当前订阅生成配置失败（%v），回退默认直连配置", err)
		cfg, _ = config.RenderConfig(config.DefaultProfileYAML(), settings, secret)
	}
	if err := config.WriteFileAtomic(runtimeConfigPath, cfg, 0o600); err != nil {
		log.Fatalf("写入运行配置失败: %v", err)
	}

	ring := kernel.NewLogRing(800)
	client := kernel.NewClient(config.MihomoAPIPort, secret)
	sup := kernel.NewSupervisor(binPath, dataDir, runtimeConfigPath, ring, client)
	app := console.NewApp(console.Options{
		DataDir:      dataDir,
		ConfigPath:   runtimeConfigPath,
		SettingsPath: settingsPath,
		Bin:          binPath,
		AdminPort:    adminPort,
		Secret:       secret,
		Supervisor:   sup,
		Client:       client,
		Store:        store,
		Settings:     settings,
	})

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
	mux.HandleFunc("/api/login", auth.HandleLogin)
	mux.HandleFunc("/api/logout", auth.HandleLogout)
	mux.HandleFunc("/api/me", auth.RequireAuth(auth.HandleMe))
	mux.HandleFunc("/api/status", auth.RequireAuth(app.HandleStatus))
	mux.HandleFunc("/api/profiles", auth.RequireAuth(app.HandleProfiles))
	mux.HandleFunc("/api/profiles/", auth.RequireAuth(app.HandleProfileAction))
	mux.HandleFunc("/api/proxies", auth.RequireAuth(app.HandleProxies))
	mux.HandleFunc("/api/proxies/select", auth.RequireAuth(app.HandleSelectProxy))
	mux.HandleFunc("/api/proxies/test", auth.RequireAuth(app.HandleTestProxy))
	mux.HandleFunc("/api/rules", auth.RequireAuth(app.HandleRules))
	mux.HandleFunc("/api/connections", auth.RequireAuth(app.HandleConnections))
	mux.HandleFunc("/api/connections/", auth.RequireAuth(app.HandleConnectionAction))
	mux.HandleFunc("/api/logs", auth.RequireAuth(app.HandleLogs))
	mux.HandleFunc("/api/settings", auth.RequireAuth(app.HandleSettings))
	mux.HandleFunc("/api/core/restart", auth.RequireAuth(app.HandleRestart))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		b := web.HTML()
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
