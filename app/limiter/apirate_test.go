package limiter

import (
	"testing"
)

func TestAPIRateLimiter_Disabled(t *testing.T) {
	r := NewAPIRateLimiter(APIRateLimitConfig{Rate: 0})
	if r != nil {
		t.Fatal("expected nil when Rate is 0")
	}
}

func TestAPIRateLimiter_Allow(t *testing.T) {
	r := NewAPIRateLimiter(APIRateLimitConfig{
		Rate:  2,
		Burst: 2,
	})
	defer r.Stop()
	if r == nil {
		t.Fatal("expected non-nil limiter")
	}
	ip := "192.168.1.1"
	if !r.Allow(ip) {
		t.Error("first request should be allowed")
	}
	if !r.Allow(ip) {
		t.Error("second request (burst) should be allowed")
	}
	if r.Allow(ip) {
		t.Error("third request should be rate limited")
	}
}

func TestAPIRateLimiter_Whitelist(t *testing.T) {
	r := NewAPIRateLimiter(APIRateLimitConfig{
		Rate:        0.001,
		Burst:       1,
		WhitelistIP: []string{"127.0.0.1", "::1"},
	})
	defer r.Stop()
	for i := 0; i < 10; i++ {
		if !r.Allow("127.0.0.1") {
			t.Errorf("whitelisted 127.0.0.1 should be allowed (req %d)", i+1)
		}
		if !r.Allow("[::1]:12345") {
			t.Errorf("whitelisted ::1 should be allowed (req %d)", i+1)
		}
	}
	if !r.Allow("10.0.0.1") {
		t.Error("first request from 10.0.0.1 should be allowed")
	}
	if r.Allow("10.0.0.1") {
		t.Error("second request from 10.0.0.1 should be limited")
	}
}
