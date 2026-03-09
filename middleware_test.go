package fileprocesor

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func fixedTime(sec int) time.Time { return time.Unix(int64(sec), 0) }

func TestSecurityHeaders(t *testing.T) {
	handler := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	checks := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":       "DENY",
		"X-XSS-Protection":      "0",
		"Referrer-Policy":       "strict-origin-when-cross-origin",
	}
	for header, want := range checks {
		if got := rr.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
}

func TestCORS_AllowedOrigin(t *testing.T) {
	handler := CORS([]string{"https://app.example.com"})(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	)

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Origin", "https://app.example.com")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("ACAO = %q, want %q", got, "https://app.example.com")
	}
}

func TestCORS_DisallowedOrigin(t *testing.T) {
	handler := CORS([]string{"https://app.example.com"})(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	)

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("ACAO should be empty for disallowed origin, got %q", got)
	}
}

func TestCORS_Preflight(t *testing.T) {
	handler := CORS([]string{"https://app.example.com"})(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("next handler should not be called on OPTIONS")
		}),
	)

	req := httptest.NewRequest("OPTIONS", "/", nil)
	req.Header.Set("Origin", "https://app.example.com")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNoContent)
	}
}

func TestRateLimit_AllowsBurst(t *testing.T) {
	handler := RateLimit(1, 3)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	)

	// Burst of 3 should be allowed.
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "10.0.0.1:1234"
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("request %d: status = %d, want 200", i, rr.Code)
		}
	}

	// 4th should be rate limited.
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusTooManyRequests {
		t.Errorf("4th request: status = %d, want 429", rr.Code)
	}
}

func TestClientIP_RemoteAddr(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "203.0.113.50:12345"
	// XFF should be ignored for non-loopback remote.
	req.Header.Set("X-Forwarded-For", "attacker-spoofed-ip")

	ip := clientIP(req)
	if ip != "203.0.113.50" {
		t.Errorf("clientIP = %q, want %q", ip, "203.0.113.50")
	}
}

func TestClientIP_Loopback_TrustsXFF(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	req.Header.Set("X-Forwarded-For", "198.51.100.10, 10.0.0.1")

	ip := clientIP(req)
	if ip != "198.51.100.10" {
		t.Errorf("clientIP = %q, want %q", ip, "198.51.100.10")
	}
}

func TestClientIP_Loopback_FallsBackToRemoteAddr(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	// No XFF or X-Real-IP.

	ip := clientIP(req)
	if ip != "127.0.0.1" {
		t.Errorf("clientIP = %q, want %q", ip, "127.0.0.1")
	}
}

func TestIPLimiter_EvictsOldest(t *testing.T) {
	l := &ipLimiter{
		visitors: make(map[string]*visitor),
		rate:     1,
		burst:    1,
	}

	// Fill to maxVisitors is impractical in test, but we can test evictOldestLocked directly.
	l.visitors["old"] = &visitor{tokens: 1, last: fixedTime(0)}
	l.visitors["new"] = &visitor{tokens: 1, last: fixedTime(1)}

	l.evictOldestLocked()

	if _, ok := l.visitors["old"]; ok {
		t.Error("expected 'old' to be evicted")
	}
	if _, ok := l.visitors["new"]; !ok {
		t.Error("expected 'new' to remain")
	}
}

func TestRequestLogging(t *testing.T) {
	handler := RequestLogging(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("hello"))
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
}
