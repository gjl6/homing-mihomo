// Package console 实现管理台的 HTTP API、登录鉴权以及配置应用流程。
package console

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"homing-mihomo/internal/config"
	"homing-mihomo/internal/kernel"
	"homing-mihomo/internal/profiles"
)

// Options 是 App 的构造参数。
type Options struct {
	DataDir      string
	ConfigPath   string
	SettingsPath string
	Bin          string
	AdminPort    int
	Secret       string
	Supervisor   *kernel.Supervisor
	Client       *kernel.Client
	Store        *profiles.Store
	Settings     config.Settings
}

// App 持有管理台运行时依赖与可变设置。
type App struct {
	dataDir      string
	configPath   string
	settingsPath string
	bin          string
	adminPort    int
	secret       string
	sup          *kernel.Supervisor
	client       *kernel.Client
	store        *profiles.Store
	applyMu      sync.Mutex
	settingsMu   sync.RWMutex
	settings     config.Settings
}

// NewApp 装配管理台。
func NewApp(o Options) *App {
	return &App{
		dataDir:      o.DataDir,
		configPath:   o.ConfigPath,
		settingsPath: o.SettingsPath,
		bin:          o.Bin,
		adminPort:    o.AdminPort,
		secret:       o.Secret,
		sup:          o.Supervisor,
		client:       o.Client,
		store:        o.Store,
		settings:     o.Settings,
	}
}

// Settings 返回当前设置快照。
func (a *App) Settings() config.Settings {
	a.settingsMu.RLock()
	defer a.settingsMu.RUnlock()
	return a.settings
}

// ApplySettings 应用并持久化新设置。
func (a *App) ApplySettings(s config.Settings) error {
	if err := config.ValidateSettings(s, a.adminPort); err != nil {
		return err
	}
	a.applyMu.Lock()
	defer a.applyMu.Unlock()
	p, ok := a.store.Active()
	if !ok {
		return fmt.Errorf("没有启用的订阅")
	}
	data, err := a.store.Read(p.ID)
	if err != nil {
		return err
	}
	if err := a.applyConfigLocked(data, s); err != nil {
		return err
	}
	if err := config.SaveSettings(a.settingsPath, s); err != nil {
		return fmt.Errorf("运行配置已生效，但保存设置失败: %w", err)
	}
	a.settingsMu.Lock()
	a.settings = s
	a.settingsMu.Unlock()
	return nil
}

// ActivateProfile 应用指定订阅并设为当前启用。
func (a *App) ActivateProfile(id string) error {
	a.applyMu.Lock()
	defer a.applyMu.Unlock()
	data, err := a.store.Read(id)
	if err != nil {
		return err
	}
	s := a.Settings()
	if err := a.applyConfigLocked(data, s); err != nil {
		return err
	}
	return a.store.SetActive(id)
}

// RestartCore 重启内核并等待其恢复健康。
func (a *App) RestartCore() error {
	a.applyMu.Lock()
	defer a.applyMu.Unlock()
	a.sup.Restart()
	if !a.sup.WaitHealthy(12 * time.Second) {
		return fmt.Errorf("内核重启后未恢复: %s", a.sup.LogTail(20))
	}
	return nil
}

func (a *App) applyConfigLocked(profile []byte, s config.Settings) error {
	cfg, err := config.RenderConfig(profile, s, a.secret)
	if err != nil {
		return err
	}
	if err := a.validateConfig(cfg); err != nil {
		return err
	}
	old, _ := os.ReadFile(a.configPath)
	backup := filepath.Join(a.dataDir, "backups", fmt.Sprintf("config-%d.yaml", time.Now().Unix()))
	if len(old) > 0 {
		_ = config.WriteFileAtomic(backup, old, 0o600)
	}
	if err := config.WriteFileAtomic(a.configPath, cfg, 0o600); err != nil {
		return fmt.Errorf("写入运行配置失败: %w", err)
	}
	a.sup.Restart()
	if a.sup.WaitHealthy(12 * time.Second) {
		return nil
	}
	msg := a.sup.LogTail(20)
	if len(old) > 0 {
		_ = config.WriteFileAtomic(a.configPath, old, 0o600)
		a.sup.Restart()
		a.sup.WaitHealthy(8 * time.Second)
	}
	if msg == "" {
		msg = "内核没有恢复健康"
	}
	return fmt.Errorf("配置应用失败，已回滚: %s", msg)
}

func (a *App) validateConfig(cfg []byte) error {
	dir := filepath.Join(a.dataDir, "runtime")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	testPath := filepath.Join(dir, "config.test.yaml")
	if err := os.WriteFile(testPath, cfg, 0o600); err != nil {
		return err
	}
	defer os.Remove(testPath)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, a.bin, "-t", "-d", filepath.Join(a.dataDir, "mihomo"), "-f", testPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		text := strings.TrimSpace(string(out))
		if len(text) > 600 {
			text = text[len(text)-600:]
		}
		if text == "" {
			text = err.Error()
		}
		return fmt.Errorf("配置校验失败: %s", text)
	}
	return nil
}
