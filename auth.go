package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type account struct {
	Username     string    `json:"username"`
	PasswordHash string    `json:"passwordHash"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

var (
	accMu    sync.Mutex
	acct     *account
	acctPath = dataDir + "/admin.json"

	sessMu sync.Mutex
	sess   = map[string]time.Time{}
)

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

func loadAccount() error {
	accMu.Lock()
	defer accMu.Unlock()
	if b, err := os.ReadFile(acctPath); err == nil {
		var a account
		if json.Unmarshal(b, &a) == nil && a.PasswordHash != "" {
			acct = &a
			return nil
		}
	}
	if strings.TrimSpace(adminPassword) == "" {
		return fmt.Errorf("首次启动需要 ADMIN_PASSWORD 初始化管理账户")
	}
	a := &account{
		Username:     adminUser,
		PasswordHash: hashPassword(adminPassword),
		UpdatedAt:    time.Now(),
	}
	b, _ := json.MarshalIndent(a, "", "  ")
	if err := writeFileAtomic(acctPath, b, 0o600); err != nil {
		return err
	}
	acct = a
	return nil
}

func newToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func issueSession(w http.ResponseWriter) {
	tok := newToken()
	sessMu.Lock()
	sess[tok] = time.Now().Add(24 * time.Hour)
	sessMu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name: "homingmihomo", Value: tok, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, MaxAge: 86400,
	})
}

func authOK(r *http.Request) bool {
	c, err := r.Cookie("homingmihomo")
	if err != nil {
		return false
	}
	sessMu.Lock()
	exp, ok := sess[c.Value]
	sessMu.Unlock()
	return ok && time.Now().Before(exp)
}

func requireAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authOK(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		h(w, r)
	}
}
