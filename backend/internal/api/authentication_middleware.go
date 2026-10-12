package api

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// Only the immediate peer can authorize use of forwarding headers. Walk the
// chain from the trusted edge, stopping at the first untrusted client.
func trustedClientIP(remote, forwarded string, trusted []netip.Prefix) string {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return remote
	}
	peer = peer.Unmap()
	contains := func(addr netip.Addr) bool {
		for _, prefix := range trusted {
			if prefix.Contains(addr) {
				return true
			}
		}
		return false
	}
	if !contains(peer) || forwarded == "" {
		return peer.String()
	}
	parts := strings.Split(forwarded, ",")
	if len(parts) > 32 {
		return peer.String()
	}
	current := peer
	for i := len(parts) - 1; i >= 0; i-- {
		if !contains(current) {
			break
		}
		next, err := netip.ParseAddr(strings.TrimSpace(parts[i]))
		if err != nil {
			return peer.String()
		}
		current = next.Unmap()
	}
	return current.String()
}

func (s *Server) trustedProxyMiddleware(next http.Handler) http.Handler {
	var trusted []netip.Prefix
	for _, raw := range s.serverCfg.Authentication.TrustedProxyCIDRs {
		if p, err := netip.ParsePrefix(raw); err == nil {
			trusted = append(trusted, p)
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = trustedClientIP(r.RemoteAddr, r.Header.Get("X-Forwarded-For"), trusted)
		next.ServeHTTP(w, r)
	})
}

type authenticationWindow struct {
	count   int
	expires time.Time
}
type authenticationLimiter struct {
	mu      sync.Mutex
	windows map[string]authenticationWindow
	swept   time.Time
}

func (l *authenticationLimiter) allow(key string, limit int, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.windows == nil {
		l.windows = make(map[string]authenticationWindow)
	}
	if now.Sub(l.swept) >= time.Minute {
		for k, v := range l.windows {
			if !now.Before(v.expires) {
				delete(l.windows, k)
			}
		}
		l.swept = now
	}
	w, exists := l.windows[key]
	if !exists && len(l.windows) >= 10000 {
		return false
	}
	if !now.Before(w.expires) {
		w = authenticationWindow{expires: now.Add(time.Minute)}
	}
	if w.count >= limit {
		return false
	}
	w.count++
	l.windows[key] = w
	return true
}

func (s *Server) authenticationAllowed(w http.ResponseWriter, r *http.Request, key string, limit int) bool {
	if s.isLocal() {
		return true
	}
	if s.authLimiter.allow(key, limit, time.Now()) {
		return true
	}
	w.Header().Set("Retry-After", "60")
	w.Header().Set("Cache-Control", "no-store")
	s.writeProblemWithType(w, r, http.StatusTooManyRequests, "urn:linguaflow:authentication-rate-limited", "authentication_rate_limited", "认证请求过于频繁，请稍后重试")
	return false
}

func (s *Server) authenticationRateMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		protected := strings.HasPrefix(p, "/api/v1/auth/") || strings.HasPrefix(p, "/api/v1/admin/auth-connections") || strings.HasPrefix(p, "/api/v1/users/me/login-") || p == "/api/v1/users/me/password"
		if protected && p != "/api/v1/auth/capabilities" && !strings.HasPrefix(p, "/api/v1/auth/connections/") {
			limit := s.serverCfg.Authentication.RateLimitPerMinute
			if limit <= 0 {
				limit = 60
			}
			if !s.authenticationAllowed(w, r, "ip:"+r.RemoteAddr, limit) {
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) passwordAttemptAllowed(w http.ResponseWriter, r *http.Request, identifier string) bool {
	digest := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(identifier))))
	return s.authenticationAllowed(w, r, "password:"+r.RemoteAddr+":"+hex.EncodeToString(digest[:]), 10)
}
