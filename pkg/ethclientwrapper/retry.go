package ethclientwrapper

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum/rpc"
)

const (
	// DefaultRetryDelay is the base delay applied before the first retry.
	DefaultRetryDelay = time.Second

	// maxRetryDelay caps the exponential backoff between retries.
	maxRetryDelay = 30 * time.Second

	// rpcErrCodeLimitExceeded is the JSON-RPC error code endpoints return when
	// the client is being rate limited. Unlike other server-side errors, it is
	// worth retrying.
	rpcErrCodeLimitExceeded = -32005
)

// retryConfig describes how transient RPC failures are retried. Its zero value
// performs no retries.
type retryConfig struct {
	// maxRetries is the number of retries attempted after the initial call.
	maxRetries int
	baseDelay  time.Duration
	maxDelay   time.Duration

	// sleep, jitter and onRetry are injectable to keep the backoff testable.
	sleep   func(context.Context, time.Duration) error
	jitter  func(time.Duration) time.Duration
	onRetry func(attempt int, delay time.Duration, err error)
}

// newRetryConfig returns a config retrying up to maxRetries times, starting
// with baseDelay and doubling it up to maxRetryDelay.
func newRetryConfig(maxRetries int, baseDelay time.Duration) retryConfig {
	return retryConfig{
		maxRetries: maxRetries,
		baseDelay:  baseDelay,
		maxDelay:   maxRetryDelay,
	}
}

// withDefaults fills in the fields left unset by the caller.
func (c retryConfig) withDefaults() retryConfig {
	if c.maxRetries < 0 {
		c.maxRetries = 0
	}
	if c.baseDelay <= 0 {
		c.baseDelay = DefaultRetryDelay
	}
	if c.maxDelay < c.baseDelay {
		c.maxDelay = c.baseDelay
	}
	if c.sleep == nil {
		c.sleep = sleepCtx
	}
	if c.jitter == nil {
		c.jitter = jitter
	}
	if c.onRetry == nil {
		c.onRetry = func(int, time.Duration, error) {}
	}
	return c
}

// retryCall calls fn, retrying transient failures with an exponential backoff.
// It gives up as soon as the error is not transient, the retries are exhausted
// or ctx is done, and then returns the error of the last attempt.
func retryCall[T any](ctx context.Context, cfg retryConfig, fn func() (T, error)) (T, error) {
	cfg = cfg.withDefaults()

	for attempt := 0; ; attempt++ {
		result, err := fn()
		if err == nil {
			return result, nil
		}

		if attempt >= cfg.maxRetries || !isRetryable(err) || ctx.Err() != nil {
			return result, err
		}

		delay := cfg.jitter(backoffDelay(attempt+1, cfg.baseDelay, cfg.maxDelay))
		cfg.onRetry(attempt+1, delay, err)

		if sleepErr := cfg.sleep(ctx, delay); sleepErr != nil {
			return result, err
		}
	}
}

// backoffDelay returns the delay before the given 1-based retry attempt,
// doubling base per attempt without ever exceeding max.
func backoffDelay(attempt int, base, maxDelay time.Duration) time.Duration {
	delay := base
	for i := 1; i < attempt; i++ {
		delay *= 2
		// A delay that is no longer positive means the doubling overflowed.
		if delay <= 0 || delay >= maxDelay {
			return maxDelay
		}
	}
	return delay
}

// jitter spreads the delay over the second half of the backoff window so that
// repeated retries do not hit the endpoint in lockstep.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}

// sleepCtx waits for d, returning early if ctx is done.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// isRetryable reports whether err is a transient failure that a later attempt
// may recover from, such as a dropped connection or a timed out TLS handshake.
func isRetryable(err error) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	// The endpoint answered with an HTTP error: retry only if it is temporary.
	var httpErr rpc.HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode == 429 || httpErr.StatusCode >= 500
	}

	// The endpoint answered with a JSON-RPC error, so the request reached it and
	// replaying it would fail the same way, unless we are being rate limited.
	var rpcErr rpc.Error
	if errors.As(err, &rpcErr) {
		return rpcErr.ErrorCode() == rpcErrCodeLimitExceeded
	}

	// Transport level failures: timeouts, TLS handshake failures, DNS errors.
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}

	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}

	return errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNABORTED) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ETIMEDOUT)
}
