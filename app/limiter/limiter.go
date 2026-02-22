package limiter

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

type Limiter struct {
	InboundInfo *sync.Map // Key: Tag, Value: *InboundInfo
	stopChan    chan struct{}
	wg          sync.WaitGroup
}

type InboundInfo struct {
	Tag          string
	UserIPs      *sync.Map // Key: Email, Value: *{Key: IP, Value: Timestamp}
	RateLimiters *sync.Map // Key: Email, Value: *rate.Limiter
}

func New() *Limiter {
	l := &Limiter{
		InboundInfo: new(sync.Map),
		stopChan:    make(chan struct{}),
	}

	l.wg.Add(1)

	go l.Start(5 * time.Minute)

	return l
}

func (l *Limiter) Start(interval time.Duration) {
	defer l.wg.Done()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			l.Clean(10 * time.Minute)
		case <-l.stopChan:
			return
		}
	}
}

func (l *Limiter) Stop() {
	close(l.stopChan)
	l.wg.Wait()
}

func (l *Limiter) Get(tag string, email string, ipLimit uint32, rateLimit uint64, ip string) (*rate.Limiter, bool, bool) {
	inboundInfoValue, _ := l.InboundInfo.LoadOrStore(tag, &InboundInfo{
		Tag:          tag,
		UserIPs:      new(sync.Map),
		RateLimiters: new(sync.Map),
	})
	inboundInfo := inboundInfoValue.(*InboundInfo)

	ipMapsValue, _ := inboundInfo.UserIPs.LoadOrStore(email, new(sync.Map))
	ipMaps := ipMapsValue.(*sync.Map)

	_, ipExists := ipMaps.LoadOrStore(ip, new(int64))

	// Record IP access
	timestamp := time.Now().Unix()
	ipMaps.Store(ip, timestamp)

	// Enforce IP limit
	if !ipExists && ipLimit > 0 {
		var ipCount uint32

		ipMaps.Range(func(_, _ interface{}) bool {
			ipCount++
			return true
		})

		if ipCount > ipLimit {
			ipMaps.Delete(ip)

			return nil, false, true
		}
	}

	// Enforce rate limit
	if rateLimit > 0 {
		if rateLimiter, emailExists := inboundInfo.RateLimiters.Load(email); emailExists {
			return rateLimiter.(*rate.Limiter), true, false
		}

		bucket := rate.NewLimiter(rate.Limit(rateLimit), int(rateLimit))
		inboundInfo.RateLimiters.Store(email, bucket)

		return bucket, true, false
	}

	return nil, false, false
}

func (l *Limiter) Clean(timeout time.Duration) {
	expirationTime := time.Now().Add(-timeout).Unix()

	l.InboundInfo.Range(func(_, value interface{}) bool {
		inboundInfo := value.(*InboundInfo)

		var emailsToDelete []string

		inboundInfo.UserIPs.Range(func(key, value interface{}) bool {
			email := key.(string)
			ipMaps := value.(*sync.Map)

			var ipsToDelete []string

			ipMaps.Range(func(key, value interface{}) bool {
				ip := key.(string)
				ipTimestamp := value.(int64)

				if ipTimestamp < expirationTime {
					ipsToDelete = append(ipsToDelete, ip)
				}

				return true
			})

			for _, ip := range ipsToDelete {
				ipMaps.Delete(ip)
			}

			var ipCount uint32

			ipMaps.Range(func(_, _ interface{}) bool {
				ipCount++
				return true
			})

			if ipCount == 0 {
				emailsToDelete = append(emailsToDelete, email)
			}

			return true
		})

		for _, email := range emailsToDelete {
			inboundInfo.UserIPs.Delete(email)
			inboundInfo.RateLimiters.Delete(email)
		}

		return true
	})
}
