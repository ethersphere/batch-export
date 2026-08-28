package ethclientwrapper

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"syscall"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/rpc"
)

// errTLSHandshakeTimeout mimics the error surfaced by net/http when the TLS
// handshake with the RPC endpoint times out.
var errTLSHandshakeTimeout = &url.Error{
	Op:  "Post",
	URL: "https://gno.swarm1.ethswarm.org/",
	Err: errors.New("net/http: TLS handshake timeout"),
}

type fakeRPCError struct{ code int }

func (e fakeRPCError) Error() string  { return "json-rpc error" }
func (e fakeRPCError) ErrorCode() int { return e.code }

// testConfig returns a config with deterministic backoff that records the
// delays it would have slept for instead of sleeping.
func testConfig(maxRetries int, delays *[]time.Duration) retryConfig {
	cfg := newRetryConfig(maxRetries, time.Second)
	cfg.jitter = func(d time.Duration) time.Duration { return d }
	cfg.sleep = func(_ context.Context, d time.Duration) error {
		*delays = append(*delays, d)
		return nil
	}
	return cfg
}

func TestRetryCallSucceedsOnFirstAttempt(t *testing.T) {
	t.Parallel()

	var delays []time.Duration
	calls := 0

	got, err := retryCall(context.Background(), testConfig(5, &delays), func() (int, error) {
		calls++
		return 42, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 42 {
		t.Errorf("got %d, want 42", got)
	}
	if calls != 1 {
		t.Errorf("made %d calls, want 1", calls)
	}
	if len(delays) != 0 {
		t.Errorf("slept %v, want no sleeps", delays)
	}
}

func TestRetryCallSucceedsAfterTransientFailures(t *testing.T) {
	t.Parallel()

	var (
		delays  []time.Duration
		retries []int
		calls   int
	)

	cfg := testConfig(5, &delays)
	cfg.onRetry = func(attempt int, _ time.Duration, _ error) { retries = append(retries, attempt) }

	got, err := retryCall(context.Background(), cfg, func() (string, error) {
		calls++
		if calls < 3 {
			return "", errTLSHandshakeTimeout
		}
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "ok" {
		t.Errorf("got %q, want %q", got, "ok")
	}
	if calls != 3 {
		t.Errorf("made %d calls, want 3", calls)
	}
	if want := []time.Duration{time.Second, 2 * time.Second}; !equalDurations(delays, want) {
		t.Errorf("slept %v, want %v", delays, want)
	}
	if want := []int{1, 2}; !equalInts(retries, want) {
		t.Errorf("reported retries %v, want %v", retries, want)
	}
}

func TestRetryCallExhaustsRetries(t *testing.T) {
	t.Parallel()

	var delays []time.Duration
	calls := 0

	_, err := retryCall(context.Background(), testConfig(2, &delays), func() (int, error) {
		calls++
		return 0, errTLSHandshakeTimeout
	})
	if !errors.Is(err, errTLSHandshakeTimeout) {
		t.Fatalf("got error %v, want %v", err, errTLSHandshakeTimeout)
	}
	if calls != 3 {
		t.Errorf("made %d calls, want 3 (1 attempt + 2 retries)", calls)
	}
	if want := []time.Duration{time.Second, 2 * time.Second}; !equalDurations(delays, want) {
		t.Errorf("slept %v, want %v", delays, want)
	}
}

func TestRetryCallDisabled(t *testing.T) {
	t.Parallel()

	var delays []time.Duration
	calls := 0

	_, err := retryCall(context.Background(), testConfig(0, &delays), func() (int, error) {
		calls++
		return 0, errTLSHandshakeTimeout
	})
	if !errors.Is(err, errTLSHandshakeTimeout) {
		t.Fatalf("got error %v, want %v", err, errTLSHandshakeTimeout)
	}
	if calls != 1 {
		t.Errorf("made %d calls, want 1", calls)
	}
}

func TestRetryCallZeroValueConfigDoesNotRetry(t *testing.T) {
	t.Parallel()

	calls := 0

	_, err := retryCall(context.Background(), retryConfig{}, func() (int, error) {
		calls++
		return 0, errTLSHandshakeTimeout
	})
	if !errors.Is(err, errTLSHandshakeTimeout) {
		t.Fatalf("got error %v, want %v", err, errTLSHandshakeTimeout)
	}
	if calls != 1 {
		t.Errorf("made %d calls, want 1", calls)
	}
}

func TestRetryCallDoesNotRetryPermanentErrors(t *testing.T) {
	t.Parallel()

	var delays []time.Duration
	permanent := fakeRPCError{code: -32602} // invalid params
	calls := 0

	_, err := retryCall(context.Background(), testConfig(5, &delays), func() (int, error) {
		calls++
		return 0, permanent
	})
	if !errors.Is(err, permanent) {
		t.Fatalf("got error %v, want %v", err, permanent)
	}
	if calls != 1 {
		t.Errorf("made %d calls, want 1", calls)
	}
	if len(delays) != 0 {
		t.Errorf("slept %v, want no sleeps", delays)
	}
}

func TestRetryCallStopsWhenContextIsCanceledDuringBackoff(t *testing.T) {
	t.Parallel()

	cfg := newRetryConfig(5, time.Second)
	cfg.jitter = func(d time.Duration) time.Duration { return d }
	cfg.sleep = func(context.Context, time.Duration) error { return context.Canceled }

	calls := 0
	_, err := retryCall(context.Background(), cfg, func() (int, error) {
		calls++
		return 0, errTLSHandshakeTimeout
	})
	if !errors.Is(err, errTLSHandshakeTimeout) {
		t.Fatalf("got error %v, want the last call error %v", err, errTLSHandshakeTimeout)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("got error %v, want it to report the cancellation too", err)
	}
	if calls != 1 {
		t.Errorf("made %d calls, want 1", calls)
	}
}

func TestRetryCallStopsWhenContextIsAlreadyCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var delays []time.Duration
	calls := 0

	_, err := retryCall(ctx, testConfig(5, &delays), func() (int, error) {
		calls++
		return 0, errTLSHandshakeTimeout
	})
	if !errors.Is(err, errTLSHandshakeTimeout) {
		t.Fatalf("got error %v, want %v", err, errTLSHandshakeTimeout)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("got error %v, want it to report the cancellation too", err)
	}
	if calls != 1 {
		t.Errorf("made %d calls, want 1", calls)
	}
	if len(delays) != 0 {
		t.Errorf("slept %v, want no sleeps", delays)
	}
}

func TestBackoffDelay(t *testing.T) {
	t.Parallel()

	const (
		base     = time.Second
		maxDelay = 30 * time.Second
	)

	for _, tc := range []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: 1, want: time.Second},
		{attempt: 2, want: 2 * time.Second},
		{attempt: 3, want: 4 * time.Second},
		{attempt: 4, want: 8 * time.Second},
		{attempt: 5, want: 16 * time.Second},
		{attempt: 6, want: maxDelay},
		{attempt: 100, want: maxDelay},   // no overflow
		{attempt: 10000, want: maxDelay}, // no overflow
	} {
		if got := backoffDelay(tc.attempt, base, maxDelay); got != tc.want {
			t.Errorf("backoffDelay(%d) = %v, want %v", tc.attempt, got, tc.want)
		}
	}
}

func TestIsRetryable(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "tls handshake timeout", err: errTLSHandshakeTimeout, want: true},
		{name: "wrapped tls handshake timeout", err: errors.Join(errors.New("failed to retrieve logs"), errTLSHandshakeTimeout), want: true},
		{name: "dns failure", err: &net.DNSError{Err: "no such host", IsNotFound: true}, want: true},
		{name: "unexpected eof", err: io.ErrUnexpectedEOF, want: true},
		{name: "eof", err: io.EOF, want: true},
		{name: "connection reset", err: syscall.ECONNRESET, want: true},
		{name: "connection refused", err: syscall.ECONNREFUSED, want: true},
		{name: "broken pipe", err: syscall.EPIPE, want: true},
		{name: "http 408", err: rpc.HTTPError{StatusCode: 408, Status: "408 Request Timeout"}, want: true},
		{name: "http 429", err: rpc.HTTPError{StatusCode: 429, Status: "429 Too Many Requests"}, want: true},
		{name: "http 502", err: rpc.HTTPError{StatusCode: 502, Status: "502 Bad Gateway"}, want: true},
		{name: "http 400", err: rpc.HTTPError{StatusCode: 400, Status: "400 Bad Request"}, want: false},
		{name: "rpc limit exceeded", err: fakeRPCError{code: -32005}, want: true},
		{name: "rpc invalid params", err: fakeRPCError{code: -32602}, want: false},
		{name: "rpc internal error", err: fakeRPCError{code: -32603}, want: false},
		{name: "context canceled", err: context.Canceled, want: false},
		{name: "context deadline exceeded", err: context.DeadlineExceeded, want: false},
		{name: "unknown error", err: errors.New("boom"), want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := isRetryable(tc.err); got != tc.want {
				t.Errorf("isRetryable(%v) = %t, want %t", tc.err, got, tc.want)
			}
		})
	}
}

func equalDurations(got, want []time.Duration) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func equalInts(got, want []int) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
