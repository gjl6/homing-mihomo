package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Profile struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"` // builtin / url / file
	URL       string `json:"url,omitempty"`
	UpdatedAt string `json:"updatedAt"`
	Size      int    `json:"size"`
}

type profileState struct {
	Active   string    `json:"active"`
	Profiles []Profile `json:"profiles"`
}

type ProfileStore struct {
	mu    sync.Mutex
	dir   string
	path  string
	state profileState
}

func newProfileStore(dataDir string) (*ProfileStore, error) {
	dir := filepath.Join(dataDir, "profiles")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &ProfileStore{dir: dir, path: filepath.Join(dataDir, "profiles.json")}
	if b, err := os.ReadFile(s.path); err == nil {
		_ = json.Unmarshal(b, &s.state)
	}
	if len(s.state.Profiles) == 0 {
		p, err := s.addYAML("默认直连", "builtin", "", defaultProfileYAML())
		if err != nil {
			return nil, err
		}
		s.state.Active = p.ID
		if err := s.save(); err != nil {
			return nil, err
		}
	}
	if s.state.Active == "" {
		s.state.Active = s.state.Profiles[0].ID
		_ = s.save()
	}
	return s, nil
}

func (s *ProfileStore) save() error {
	b, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.path, b, 0o600)
}

func (s *ProfileStore) List() ([]Profile, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]Profile(nil), s.state.Profiles...)
	return out, s.state.Active
}

func (s *ProfileStore) Get(id string) (Profile, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.state.Profiles {
		if p.ID == id {
			return p, true
		}
	}
	return Profile{}, false
}

func (s *ProfileStore) Active() (Profile, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.state.Profiles {
		if p.ID == s.state.Active {
			return p, true
		}
	}
	return Profile{}, false
}

func (s *ProfileStore) Read(id string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.state.Profiles {
		if p.ID == id {
			return os.ReadFile(filepath.Join(s.dir, p.ID+".yaml"))
		}
	}
	return nil, fmt.Errorf("订阅不存在")
}

func (s *ProfileStore) SetActive(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.state.Profiles {
		if p.ID == id {
			s.state.Active = id
			return s.save()
		}
	}
	return fmt.Errorf("订阅不存在")
}

func (s *ProfileStore) WriteRaw(id string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeFileAtomic(filepath.Join(s.dir, id+".yaml"), data, 0o600)
}

func (s *ProfileStore) AddYAML(name, kind, rawURL string, data []byte) (Profile, error) {
	return s.addYAML(name, kind, rawURL, data)
}

func (s *ProfileStore) addYAML(name, kind, rawURL string, data []byte) (Profile, error) {
	// 调用方可能已经持锁；newProfileStore 初始化时没有并发，单独处理。
	return s.addYAMLUnlocked(name, kind, rawURL, data)
}

func (s *ProfileStore) addYAMLUnlocked(name, kind, rawURL string, data []byte) (Profile, error) {
	if _, err := parseProfile(data); err != nil {
		return Profile{}, err
	}
	name = cleanProfileName(name)
	if name == "" {
		name = "未命名订阅"
	}
	id := randomHex(6)
	p := Profile{
		ID:        id,
		Name:      name,
		Kind:      kind,
		URL:       rawURL,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		Size:      len(data),
	}
	if err := writeFileAtomic(filepath.Join(s.dir, id+".yaml"), data, 0o600); err != nil {
		return Profile{}, err
	}
	s.mu.Lock()
	s.state.Profiles = append(s.state.Profiles, p)
	if s.state.Active == "" {
		s.state.Active = id
	}
	err := s.save()
	s.mu.Unlock()
	return p, err
}

func (s *ProfileStore) AddURL(name, rawURL string) (Profile, error) {
	data, filename, err := downloadProfile(rawURL)
	if err != nil {
		return Profile{}, err
	}
	if strings.TrimSpace(name) == "" {
		name = filename
	}
	return s.addYAML(name, "url", rawURL, data)
}

func (s *ProfileStore) Update(id string) (Profile, error) {
	s.mu.Lock()
	idx := -1
	for i, p := range s.state.Profiles {
		if p.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		s.mu.Unlock()
		return Profile{}, fmt.Errorf("订阅不存在")
	}
	p := s.state.Profiles[idx]
	if p.Kind != "url" || p.URL == "" {
		s.mu.Unlock()
		return Profile{}, fmt.Errorf("这个订阅不是 URL 订阅，无法在线更新")
	}
	s.mu.Unlock()

	data, _, err := downloadProfile(p.URL)
	if err != nil {
		return Profile{}, err
	}
	if _, err := parseProfile(data); err != nil {
		return Profile{}, err
	}
	if err := writeFileAtomic(filepath.Join(s.dir, id+".yaml"), data, 0o600); err != nil {
		return Profile{}, err
	}
	p.Size = len(data)
	p.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	s.mu.Lock()
	for i := range s.state.Profiles {
		if s.state.Profiles[i].ID == id {
			s.state.Profiles[i] = p
			break
		}
	}
	err = s.save()
	s.mu.Unlock()
	return p, err
}

func (s *ProfileStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Active == id {
		return fmt.Errorf("不能删除当前启用的订阅，请先切换到其它订阅")
	}
	out := s.state.Profiles[:0]
	found := false
	for _, p := range s.state.Profiles {
		if p.ID == id {
			found = true
			continue
		}
		out = append(out, p)
	}
	if !found {
		return fmt.Errorf("订阅不存在")
	}
	s.state.Profiles = out
	_ = os.Remove(filepath.Join(s.dir, id+".yaml"))
	return s.save()
}

func cleanProfileName(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}

func downloadProfile(rawURL string) ([]byte, string, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, "", fmt.Errorf("订阅地址必须是 http/https URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "clash.meta/1.19.34")
	req.Header.Set("Accept", "*/*")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("订阅下载失败: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (32<<20)+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > 32<<20 {
		return nil, "", fmt.Errorf("订阅文件超过 32MB")
	}
	filename := strings.TrimSuffix(filepath.Base(u.Path), filepath.Ext(u.Path))
	if filename == "" || filename == "/" {
		filename = u.Host
	}
	return data, filename, nil
}
