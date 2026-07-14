// Package mikrotik provides a resilient connection wrapper around the RouterOS
// API and collectors that produce model.Sample and model.LogEntry values.
package mikrotik

import (
	"context"
	"crypto/tls"
	"log/slog"
	"sync"

	"github.com/go-routeros/routeros/v3"
	"github.com/jutaz/tiktelemetry/internal/config"
)

// Client wraps a *routeros.Client with lazy-connect, automatic reconnect, and
// structured logging. It is safe for sequential use from a single goroutine
// (the scrape loop). A sync.Mutex guards the underlying connection for a
// modest safety margin without meaningful contention.
type Client struct {
	cfg    config.RouterConfig
	logger *slog.Logger
	mu     sync.Mutex
	cli    *routeros.Client
}

// New creates a Client but does not connect. The connection is established
// lazily on the first call to run.
func New(cfg config.RouterConfig, logger *slog.Logger) *Client {
	return &Client{
		cfg:    cfg,
		logger: logger,
	}
}

// Run ensures a live connection, executes the given command, and returns the
// reply. On any run error the underlying connection is closed and nil'd so the
// next call initiates a fresh dial. Errors are returned to the caller — no
// infinite retry is attempted inside a single call.
func (c *Client) Run(ctx context.Context, cmd ...string) (*routeros.Reply, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cli == nil {
		if err := c.dial(ctx); err != nil {
			return nil, err
		}
	}

	reply, err := c.cli.RunArgsContext(ctx, cmd)
	if err != nil {
		c.logger.Warn("RouterOS connection lost, will reconnect",
			"address", c.cfg.Address,
			"error", err,
		)
		c.cli.Close()
		c.cli = nil
		return nil, err
	}
	return reply, nil
}

// dial establishes a new RouterOS API connection. It derives a context with
// DialTimeout when the config specifies one.
func (c *Client) dial(ctx context.Context) error {
	dialCtx := ctx
	if c.cfg.DialTimeout > 0 {
		var cancel context.CancelFunc
		dialCtx, cancel = context.WithTimeout(ctx, c.cfg.DialTimeout)
		defer cancel()
	}

	var (
		cli *routeros.Client
		err error
	)

	if c.cfg.UseTLS {
		tlsCfg := &tls.Config{
			InsecureSkipVerify: c.cfg.InsecureSkipVerify,
		}
		cli, err = routeros.DialTLSContext(dialCtx, c.cfg.Address,
			c.cfg.Username, c.cfg.Password, tlsCfg)
	} else {
		cli, err = routeros.DialContext(dialCtx, c.cfg.Address,
			c.cfg.Username, c.cfg.Password)
	}

	if err != nil {
		c.logger.Warn("failed to connect to RouterOS API",
			"address", c.cfg.Address,
			"error", err,
		)
		return err
	}

	c.cli = cli
	c.logger.Info("connected to RouterOS API",
		"address", c.cfg.Address,
	)
	return nil
}

// Close shuts down the underlying RouterOS connection, if any. It is safe to
// call multiple times.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cli != nil {
		err := c.cli.Close()
		c.cli = nil
		return err
	}
	return nil
}
