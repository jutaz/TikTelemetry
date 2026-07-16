// Package mikrotik provides a resilient connection wrapper around the RouterOS
// API and collectors that produce model.Sample and model.LogEntry values.
package mikrotik

import (
	"context"
	"crypto/tls"
	"errors"
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
		// A !trap is a per-command error (bad/unknown command, missing item,
		// permission) that the router replied with over a HEALTHY session — for
		// example querying a wireless table on a router with no radios. Return
		// it to the caller (collectors classify and often swallow it) WITHOUT
		// tearing down the connection, otherwise every scrape on a wired router
		// would log a spurious "connection lost" and reconnect needlessly.
		//
		// Any other error (transport failure, !fatal, unknown reply word) means
		// the session is gone: close and nil it so the next call re-dials.
		if isDeviceTrap(err) {
			return nil, err
		}
		c.logger.Warn("RouterOS connection lost, will reconnect",
			"address", c.cfg.Address,
			"error", err,
		)
		_ = c.cli.Close()
		c.cli = nil
		return nil, err
	}

	// Defensive cap: bound how many reply rows we hand downstream so a hostile
	// or malfunctioning router cannot force unbounded sample generation in a
	// single scrape. The rows are already read by the library; truncating here
	// still bounds the (larger) amplification into model.Sample slices.
	if capRows(reply, c.cfg.MaxReplyRows) {
		c.logger.Warn("RouterOS reply exceeded the row cap; truncating",
			"address", c.cfg.Address,
			"command", cmd[0],
			"cap", c.cfg.MaxReplyRows,
		)
	}
	return reply, nil
}

// isDeviceTrap reports whether err is a RouterOS !trap reply, i.e. a
// command-level error the device returned over an otherwise healthy connection
// (as opposed to a !fatal, which signals the session is being torn down). The
// go-routeros library models both as *routeros.DeviceError but records the
// originating sentence word, so we distinguish on that rather than matching the
// human-readable message.
func isDeviceTrap(err error) bool {
	var de *routeros.DeviceError
	if !errors.As(err, &de) || de.Sentence == nil {
		return false
	}
	return de.Sentence.Word == "!trap"
}

// capRows truncates reply.Re to at most max rows when max > 0. It returns true
// when truncation occurred, so the caller can log it. A max of 0 (or a nil
// reply) leaves the reply untouched.
func capRows(reply *routeros.Reply, max int) bool {
	if max <= 0 || reply == nil || len(reply.Re) <= max {
		return false
	}
	reply.Re = reply.Re[:max]
	return true
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
