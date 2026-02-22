package commander

import (
	"context"
	"net"
	"sync"

	"github.com/xtls/xray-core/app/limiter"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/common/signal/done"
	core "github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/outbound"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

var pendingAPIRateLimit struct {
	sync.Mutex
	rate      float64
	burst     int
	whitelist []string
}

func SetPendingAPIRateLimit(rate float64, burst int, whitelist []string) {
	pendingAPIRateLimit.Lock()
	defer pendingAPIRateLimit.Unlock()
	pendingAPIRateLimit.rate = rate
	pendingAPIRateLimit.burst = burst
	pendingAPIRateLimit.whitelist = whitelist
}

func takePendingAPIRateLimit() (rate float64, burst int, whitelist []string) {
	pendingAPIRateLimit.Lock()
	defer pendingAPIRateLimit.Unlock()
	rate = pendingAPIRateLimit.rate
	burst = pendingAPIRateLimit.burst
	whitelist = pendingAPIRateLimit.whitelist
	pendingAPIRateLimit.rate = 0
	pendingAPIRateLimit.burst = 0
	pendingAPIRateLimit.whitelist = nil
	return rate, burst, whitelist
}

// Commander is a Xray feature that provides gRPC methods to external clients.
type Commander struct {
	sync.Mutex
	server     *grpc.Server
	services   []Service
	ohm        outbound.Manager
	tag        string
	listen     string
	apiLimiter *limiter.APIRateLimiter
}

// NewCommander creates a new Commander based on the given config.
func NewCommander(ctx context.Context, config *Config) (*Commander, error) {
	c := &Commander{
		tag:    config.Tag,
		listen: config.Listen,
	}

	common.Must(core.RequireFeatures(ctx, func(om outbound.Manager) {
		c.ohm = om
	}))

	for _, rawConfig := range config.Service {
		svcConfig, err := rawConfig.GetInstance()
		if err != nil {
			return nil, err
		}
		rawService, err := common.CreateObject(ctx, svcConfig)
		if err != nil {
			return nil, err
		}
		service, ok := rawService.(Service)
		if !ok {
			return nil, errors.New("not a Service.")
		}
		c.services = append(c.services, service)
	}

	if rate, burst, whitelist := takePendingAPIRateLimit(); rate > 0 {
		c.apiLimiter = limiter.NewAPIRateLimiter(limiter.APIRateLimitConfig{
			Rate:        rate,
			Burst:       burst,
			WhitelistIP: whitelist,
		})
	}

	return c, nil
}

// Type implements common.HasType.
func (c *Commander) Type() interface{} {
	return (*Commander)(nil)
}

// Start implements common.Runnable.
func (c *Commander) Start() error {
	c.Lock()
	var opts []grpc.ServerOption
	if c.apiLimiter != nil {
		opts = append(opts,
			grpc.UnaryInterceptor(c.apiRateLimitUnaryInterceptor),
			grpc.StreamInterceptor(c.apiRateLimitStreamInterceptor),
		)
	}
	c.server = grpc.NewServer(opts...)
	for _, service := range c.services {
		service.Register(c.server)
	}
	c.Unlock()

	var listen = func(listener net.Listener) {
		if err := c.server.Serve(listener); err != nil {
			errors.LogErrorInner(context.Background(), err, "failed to start grpc server")
		}
	}

	if len(c.listen) > 0 {
		if l, err := net.Listen("tcp", c.listen); err != nil {
			errors.LogErrorInner(context.Background(), err, "API server failed to listen on ", c.listen)
			return err
		} else {
			errors.LogInfo(context.Background(), "API server listening on ", l.Addr())
			go listen(l)
		}
		return nil
	}

	listener := &OutboundListener{
		buffer: make(chan net.Conn, 4),
		done:   done.New(),
	}

	go listen(listener)

	if err := c.ohm.RemoveHandler(context.Background(), c.tag); err != nil {
		errors.LogInfoInner(context.Background(), err, "failed to remove existing handler")
	}

	return c.ohm.AddHandler(context.Background(), &Outbound{
		tag:      c.tag,
		listener: listener,
	})
}

// Close implements common.Closable.
func (c *Commander) Close() error {
	c.Lock()
	defer c.Unlock()

	if c.apiLimiter != nil {
		c.apiLimiter.Stop()
		c.apiLimiter = nil
	}
	if c.server != nil {
		c.server.Stop()
		c.server = nil
	}

	return nil
}

func (c *Commander) clientIP(ctx context.Context) string {
	if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
		return p.Addr.String()
	}
	return ""
}

func (c *Commander) apiRateLimitUnaryInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	if c.apiLimiter != nil && !c.apiLimiter.Allow(c.clientIP(ctx)) {
		return nil, status.Errorf(codes.ResourceExhausted, "api rate limit exceeded")
	}
	return handler(ctx, req)
}

func (c *Commander) apiRateLimitStreamInterceptor(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	if c.apiLimiter != nil && !c.apiLimiter.Allow(c.clientIP(ss.Context())) {
		return status.Errorf(codes.ResourceExhausted, "api rate limit exceeded")
	}
	return handler(srv, ss)
}

func init() {
	common.Must(common.RegisterConfig((*Config)(nil), func(ctx context.Context, cfg interface{}) (interface{}, error) {
		return NewCommander(ctx, cfg.(*Config))
	}))
}
