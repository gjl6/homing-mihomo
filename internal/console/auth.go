package console

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"homing-mihomo/internal/config"
)

type account struct {
	Username     string    `json:"username"`
	PasswordHash string    `json:"passwordHash"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// Auth 保存管理账户与登录会话状态。
type Auth struct {
	mu     sync.Mutex
	acct   *account
	sessMu sync.Mutex
	sess   map[string]time.Time
}

// LoadAuth 加载管理账户；首次启动时用 ADMIN_USER / ADMIN_PASSWORD 初始化。
func LoadAuth(accountPath, adminUser, adminPassword string) (*Auth, error) {
	a := &Auth{sess: map[string]time.Time{}}
	a.mu.Lock()
	defer a.mu.Unlock()
	if b, err := os.ReadFile(accountPath); err == nil {
		var acc account
		if json.Unmarshal(b, &acc) == nil && acc.PasswordHash != "" {
			a.acct = &acc
			return a, nil
		}
	}
	if strings.TrimSpace(adminPassword) == "" {
		return nil, fmt.Errorf("首次启动需要 ADMIN_PASSWORD 初始化管理账户")
	}
	acc := &account{
		Username:     adminUser,
		PasswordHash: hashPassword(adminPassword),
		UpdatedAt:    time.Now(),
	}
	b, _ := json.MarshalIndent(acc, "", "  ")
	if err := config.WriteFileAtomic(accountPath, b, 0o600); err != nil {
		return nil, err
	}
	a.acct = acc
	return a, nil
}

func (a *Auth) currentAccount() *account {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.acct
}

func pbkdf2SHA256(password, salt []byte, iter, keyLen int) []byte {
	prf := hmac.New(sha256.New, password)
	prf.Write(salt)
	prf.Write([]byte{0, 0, 0, 1})
	u := prf.Sum(nil)
	out := make([]byte, 0, keyLen)
	cur := make([]byte, len(u))
	copy(cur, u)
	for len(out) < keyLen {
		need := keyLen - len(out)
		if need > len(cur) {
			need = len(cur)
		}
		out = append(out, cur[:need]...)
		prf.Reset()
		prf.Write(cur)
		u = prf.Sum(nil)
		copy(cur, u)
	}
	return out
}

func hashPassword(pw string) string {
	salt := make([]byte, 16)
	_, _ = rand.Read(salt)
	dk := pbkdf2SHA256([]byte(pw), salt, 120000, 32)
	return fmt.Sprintf("pbkdf2$120000$%s$%s", hex.EncodeToString(salt), hex.EncodeToString(dk))
}

func verifyPassword(pw, stored string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2" {
		return false
	}
	iters, err := strconv.Atoi(parts[1])
	if err != nil || iters <= 0 {
		return false
	}
	salt, e1 := hex.DecodeString(parts[2])
	want, e2 := hex.DecodeString(parts[3])
	if e1 != nil || e2 != nil {
		return false
	}
	got := pbkdf2SHA256([]byte(pw), salt, iters, len(want))
	return subtle.ConstantTimeCompare(got, want) == 1
}

func newToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (a *Auth) issueSession(w http.ResponseWriter) {
	tok := newToken()
	a.sessMu.Lock()
	a.sess[tok] = time.Now().Add(24 * time.Hour)
	a.sessMu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name: "homingmihomo", Value: tok, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, MaxAge: 86400,
	})
}

func (a *Auth) authOK(r *http.Request) bool {
	c, err := r.Cookie("homingmihomo")
	if err != nil {
		return false
	}
	a.sessMu.Lock()
	exp, ok := a.sess[c.Value]
	a.sessMu.Unlock()
	return ok && time.Now().Before(exp)
}

// RequireAuth 包装需要登录的 handler。
func (a *Auth) RequireAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.authOK(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		h(w, r)
	}
}

// HandleLogin 处理 POST /api/login。
func (a *Auth) HandleLogin(w http.ResponseWriter, r *http.Request) {
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
	acc := a.currentAccount()
	if acc == nil {
		writeErr(w, http.StatusInternalServerError, "账户未初始化")
		return
	}
	if req.Username != "" && req.Username != acc.Username {
		time.Sleep(250 * time.Millisecond)
		writeErr(w, http.StatusUnauthorized, "用户名或密码错误")
		return
	}
	if !verifyPassword(req.Password, acc.PasswordHash) {
		time.Sleep(250 * time.Millisecond)
		writeErr(w, http.StatusUnauthorized, "用户名或密码错误")
		return
	}
	a.issueSession(w)
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "username": acc.Username})
}

// HandleLogout 处理 /api/logout。
func (a *Auth) HandleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "homingmihomo", Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

// HandleMe 返回当前登录用户名。
func (a *Auth) HandleMe(w http.ResponseWriter, r *http.Request) {
	acc := a.currentAccount()
	name := ""
	if acc != nil {
		name = acc.Username
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"username": name})
}
