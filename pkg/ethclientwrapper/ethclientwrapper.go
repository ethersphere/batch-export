package ethclientwrapper

import (
	"context"
	"sync"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethersphere/bee/v2/pkg/log"
	"golang.org/x/time/rate"
)

type Client struct {
	*ethclient.Client
	limiter *rate.Limiter
	logger  log.Logger
	rawURL  string
	mu      sync.Mutex
}

type ClientOption func(*Client)

// WithRateLimit sets the rate limit for the Ethereum client.
func WithRateLimit(requestsPerSecond int) ClientOption {
	return func(c *Client) {
		c.limiter = rate.NewLimiter(rate.Limit(requestsPerSecond), requestsPerSecond)
	}
}

// WithLogger sets a logger for the Ethereum client.
func WithLogger(logger log.Logger) ClientOption {
	return func(c *Client) {
		c.logger = logger
	}
}

// NewClient creates a new Ethereum client with possible rate limiting.
func NewClient(ctx context.Context, rawURL string, opts ...ClientOption) (*Client, error) {
	ethclient, err := ethclient.DialContext(ctx, rawURL)
	if err != nil {
		return nil, err
	}

	c := &Client{
		Client:  ethclient,
		rawURL:  rawURL,
		limiter: nil,
		logger:  log.Noop,
	}

	for _, option := range opts {
		option(c)
	}

	return c, nil
}

// Close closes the underlying Ethereum client.
func (c *Client) Close() {
	c.Client.Close()
}

func (c *Client) FilterLogs(ctx context.Context, q ethereum.FilterQuery) ([]types.Log, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.applyRateLimit(ctx); err != nil {
		return nil, err
	}

	return c.Client.FilterLogs(ctx, q)
}

// applyRateLimit checks if the limiter is set and applies the rate limit.
func (c *Client) applyRateLimit(ctx context.Context) error {
	if c.limiter != nil {
		return c.limiter.Wait(ctx)
	}
	return nil
}
