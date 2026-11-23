package kernel

import (
	"context"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// LogLine 是一行内核日志。
type LogLine struct {
	ID   int64  `json:"id"`
	Text string `json:"text"`
}

// LogRing 是固定容量的内核日志环形缓冲，同时实现 io.Writer。
type LogRing struct {
	mu      sync.Mutex
	next    int64
	lines   []LogLine
	partial string
	max     int
}

// NewLogRing 创建最多保留 max 行的日志环。
func NewLogRing(max int) *LogRing {
	return &LogRing{max: max}
}

func (r *LogRing) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.partial += string(p)
	for {
		idx := strings.IndexByte(r.partial, '\n')
		if idx < 0 {
			break
		}
		line := strings.TrimRight(r.partial[:idx], "\r")
		r.partial = r.partial[idx+1:]
		if line == "" {
			continue
		}
		r.next++
		r.lines = append(r.lines, LogLine{ID: r.next, Text: line})
		if len(r.lines) > r.max {
			r.lines = r.lines[len(r.lines)-r.max:]
		}
	}
	if len(r.partial) > 64*1024 {
		r.partial = r.partial[len(r.partial)-64*1024:]
	}
	return len(p), nil
}

// Since 返回 ID 大于 after 的日志行以及当前最大 ID。
func (r *LogRing) Since(after int64) ([]LogLine, int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	start := 0
	if after > 0 {
		for i, l := range r.lines {
			if l.ID > after {
				start = i
				break
			}
			start = len(r.lines)
		}
	}
	out := append([]LogLine(nil), r.lines[start:]...)
	return out, r.next
}

// Tail 返回最后 n 行日志文本。
func (r *LogRing) Tail(n int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n <= 0 || n > len(r.lines) {
		n = len(r.lines)
	}
	out := make([]string, 0, n)
	for _, l := range r.lines[len(r.lines)-n:] {
		out = append(out, l.Text)
	}
	return out
}

type procState struct {
	cmd       *exec.Cmd
	done      chan struct{}
	startedAt time.Time
}

// Supervisor 负责拉起、守护和重启 mihomo 内核进程。
type Supervisor struct {
	mu          sync.Mutex
	proc        *procState
	stopping    bool
	autoRestart bool
	bin         string
	configPath  string
	dataDir     string
	lastExit    string
	ring        *LogRing
	client      *Client
}

// NewSupervisor 创建内核进程守护。
func NewSupervisor(bin, dataDir, configPath string, ring *LogRing, client *Client) *Supervisor {
	return &Supervisor{
		bin:         bin,
		configPath:  configPath,
		dataDir:     dataDir,
		ring:        ring,
		client:      client,
		autoRestart: true,
	}
}

// Start 启动内核进程（已在运行则忽略）。
func (s *Supervisor) Start() {
	s.mu.Lock()
	if s.proc != nil {
		s.mu.Unlock()
		return
	}
	_ = os.MkdirAll(s.dataDir+"/mihomo", 0o700)
	cmd := exec.Command(s.bin, "-d", s.dataDir+"/mihomo", "-f", s.configPath)
	w := io.MultiWriter(os.Stdout, s.ring)
	cmd.Stdout = w
	cmd.Stderr = w
	if err := cmd.Start(); err != nil {
		s.lastExit = "启动失败: " + err.Error()
		s.mu.Unlock()
		log.Printf("mihomo 启动失败: %v", err)
		return
	}
	p := &procState{cmd: cmd, done: make(chan struct{}), startedAt: time.Now()}
	s.proc = p
	s.lastExit = ""
	s.mu.Unlock()
	log.Printf("mihomo 已启动 pid=%d", cmd.Process.Pid)
	go s.wait(p)
}

func (s *Supervisor) wait(p *procState) {
	err := p.cmd.Wait()
	close(p.done)
	s.mu.Lock()
	if s.proc == p {
		s.proc = nil
	}
	if err != nil {
		s.lastExit = err.Error()
	} else {
		s.lastExit = "mihomo 已退出"
	}
	restart := s.autoRestart && !s.stopping
	s.mu.Unlock()
	log.Printf("mihomo 退出: %v", err)
	if restart {
		time.Sleep(3 * time.Second)
		s.Start()
	}
}

// Stop 停止内核进程。
func (s *Supervisor) Stop() {
	s.mu.Lock()
	s.stopping = true
	p := s.proc
	s.mu.Unlock()
	if p == nil {
		s.mu.Lock()
		s.stopping = false
		s.mu.Unlock()
		return
	}
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Signal(os.Interrupt)
	}
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		if p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
		}
		<-p.done
	}
	s.mu.Lock()
	if s.proc == p {
		s.proc = nil
	}
	s.stopping = false
	s.mu.Unlock()
}

// Restart 重启内核进程。
func (s *Supervisor) Restart() {
	s.Stop()
	s.Start()
}

// Running 报告内核进程是否在运行。
func (s *Supervisor) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.proc != nil
}

// Uptime 返回内核进程已运行秒数。
func (s *Supervisor) Uptime() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.proc == nil {
		return 0
	}
	return int64(time.Since(s.proc.startedAt).Seconds())
}

// LastExit 返回最近一次退出/启动失败的信息。
func (s *Supervisor) LastExit() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastExit
}

// WaitHealthy 在 timeout 内轮询内核 API，直到就绪。
func (s *Supervisor) WaitHealthy(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		ok := s.client.Healthy(ctx)
		cancel()
		if ok {
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false
}

// LogTail 返回最后 n 行日志（用换行拼接）。
func (s *Supervisor) LogTail(n int) string {
	lines := s.ring.Tail(n)
	return strings.Join(lines, "\n")
}

// LogsSince 返回 ID 大于 after 的日志行以及下一个游标。
func (s *Supervisor) LogsSince(after int64) ([]LogLine, int64) {
	return s.ring.Since(after)
}
