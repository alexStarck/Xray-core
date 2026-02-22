package limiter

import (
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

type APIRateLimitConfig struct {
	Rate        float64
	Burst       int
	WhitelistIP []string
}

type APIRateLimiter struct {
	rate      rate.Limit
	burst     int
	whitelist map[string]struct{}
	limiters  *sync.Map
	stopChan  chan struct{}
	wg        sync.WaitGroup
}

func NewAPIRateLimiter(config APIRateLimitConfig) *APIRateLimiter {
	if config.Rate <= 0 {
		return nil
	}
	burst := config.Burst
	if burst <= 0 {
		burst = 1
	}
	rl := &APIRateLimiter{
		rate:      rate.Limit(config.Rate),
		burst:     burst,
		whitelist: make(map[string]struct{}),
		limiters:  new(sync.Map),
		stopChan:  make(chan struct{}),
	}
	for _, s := range config.WhitelistIP {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if ip := net.ParseIP(s); ip != nil {
			rl.whitelist[ip.String()] = struct{}{}
		} else if _, _, err := net.ParseCIDR(s); err == nil {
			rl.whitelist[s] = struct{}{}
		}
	}
	rl.wg.Add(1)
	go rl.cleanup(5 * time.Minute)
	return rl
}

func (r *APIRateLimiter) cleanup(interval time.Duration) {
	defer r.wg.Done()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			_ = interval
		case <-r.stopChan:
			return
		}
	}
}

func (r *APIRateLimiter) Stop() {
	if r == nil {
		return
	}
	close(r.stopChan)
	r.wg.Wait()
}

func (r *APIRateLimiter) Allow(clientIP string) bool {
	if r == nil {
		return true
	}
	ipStr := clientIP
	if host, _, err := net.SplitHostPort(clientIP); err == nil {
		ipStr = host
	}
	ip := net.ParseIP(ipStr)
	if ip != nil {
		if _, ok := r.whitelist[ip.String()]; ok {
			return true
		}
		for cidr := range r.whitelist {
			if _, network, err := net.ParseCIDR(cidr); err == nil && network.Contains(ip) {
				return true
			}
		}
	}
	key := ipStr
	val, _ := r.limiters.LoadOrStore(key, rate.NewLimiter(r.rate, r.burst))
	return val.(*rate.Limiter).Allow()
}
