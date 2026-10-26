package httpapi

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mario/myporn/internal/secret"
)

const sessionCookie = "mp_session"

const sessionTTL = 30 * 24 * time.Hour

type loginLimiter struct {
	mu       sync.Mutex
	fails    map[string]int
	lockedAt map[string]time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{fails: map[string]int{}, lockedAt: map[string]time.Time{}}
}

func (l *loginLimiter) locked(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	t, ok := l.lockedAt[ip]
	if !ok {
		return false
	}
	if time.Since(t) > 5*time.Minute {
		delete(l.lockedAt, ip)
		delete(l.fails, ip)
		return false
	}
	return true
}

func (l *loginLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fails[ip]++
	if l.fails[ip] >= 5 {
		l.lockedAt[ip] = time.Now()
	}
}

func (l *loginLimiter) reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, ip)
	delete(l.lockedAt, ip)
}

func clientIP(r *http.Request) string {
	if v := r.Header.Get("X-Real-IP"); v != "" {
		return v
	}
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		return strings.TrimSpace(strings.Split(v, ",")[0])
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		return host[:i]
	}
	return host
}

type loginRequest struct {
	Password string `json:"password"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if s.limiter.locked(ip) {
		writeErr(w, http.StatusTooManyRequests, "too_many_attempts", "尝试次数过多，请 5 分钟后再试")
		return
	}
	var req loginRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求体非法")
		return
	}
	if req.Password != s.cfg.Password {
		s.limiter.fail(ip)
		writeErr(w, http.StatusUnauthorized, "invalid_password", "密码错误")
		return
	}
	token, err := secret.RandomToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "生成会话失败")
		return
	}
	if err := s.st.CreateSession(token, sessionTTL); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "保存会话失败")
		return
	}
	s.limiter.reset(ip)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = s.st.DeleteSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMe(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true})
}

func (s *Server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil || c.Value == "" {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "未登录")
			return
		}
		ok, err := s.st.SessionValid(c.Value)
		if err != nil || !ok {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "会话已过期")
			return
		}
		next.ServeHTTP(w, r)
	})
}
