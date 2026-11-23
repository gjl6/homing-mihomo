package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type App struct {
	dataDir    string
	configPath string
	bin        string
	sup        *Supervisor
	client     *mihomoClient
	store      *ProfileStore
	applyMu    sync.Mutex
	settingsMu sync.RWMutex
	settings   Settings
}

func newApp(dataDir, configPath, bin string, sup *Supervisor, client *mihomoClient, store *ProfileStore, s Settings) *App {
	return &App{
		dataDir: dataDir, configPath: configPath, bin: bin,
		sup: sup, client: client, store: store, settings: s,
	}
}

func (a *App) Settings() Settings {
	a.settingsMu.RLock()
	defer a.settingsMu.RUnlock()
	return a.settings
}

func (a *App) ApplySettings(s Settings) error {
	if err := validateSettings(s); err != nil {
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
	if err := saveSettings(s); err != nil {
		return fmt.Errorf("运行配置已生效，但保存设置失败: %w", err)
	}
	a.settingsMu.Lock()
	a.settings = s
	a.settingsMu.Unlock()
	return nil
}

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

func (a *App) RestartCore() error {
	a.applyMu.Lock()
	defer a.applyMu.Unlock()
	a.sup.Restart()
	if !a.sup.WaitHealthy(12 * time.Second) {
		return fmt.Errorf("内核重启后未恢复: %s", a.sup.LogTail(20))
	}
	return nil
}

func (a *App) applyConfigLocked(profile []byte, s Settings) error {
	cfg, err := renderConfig(profile, s, secret)
	if err != nil {
		return err
	}
	if err := a.validateConfig(cfg); err != nil {
		return err
	}
	old, _ := os.ReadFile(a.configPath)
	backup := filepath.Join(a.dataDir, "backups", fmt.Sprintf("config-%d.yaml", time.Now().Unix()))
	if len(old) > 0 {
		_ = writeFileAtomic(backup, old, 0o600)
	}
	if err := writeFileAtomic(a.configPath, cfg, 0o600); err != nil {
		return fmt.Errorf("写入运行配置失败: %w", err)
	}
	a.sup.Restart()
	if a.sup.WaitHealthy(12 * time.Second) {
		return nil
	}
	msg := a.sup.LogTail(20)
	if len(old) > 0 {
		_ = writeFileAtomic(a.configPath, old, 0o600)
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
