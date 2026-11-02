package gitea

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// RequestStats summarizes the requests finished since the last read.
type RequestStats struct {
	// Throttled is true if the server pushed back on any request.
	Throttled bool
	// RetryAfter is the longest wait the server asked for, if any.
	RetryAfter time.Duration
	// Completed counts requests the server answered without pushing back,
	// including answers such as 404 that are errors but say nothing about load.
	Completed int
}

// pushbackTransport observes every request on the HTTP client shared by the SDK
// and the raw helpers. It watches at this level because SDK errors are plain
// strings that have lost their status code. It only records what happened: it
// never retries, so it can't add traffic to a server that's already pushing back.
type pushbackTransport struct {
	next http.RoundTripper

	mu    sync.Mutex
	stats RequestStats
}

// RoundTrip implements http.RoundTripper.
func (t *pushbackTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(req)
	t.observe(resp, err)
	return resp, err
}

func (t *pushbackTransport) observe(resp *http.Response, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch {
	case err != nil:
		// Connection failures and timeouts are how a firewall ban or an
		// overloaded server looks. Cancellation only means nobody wants the
		// answer any more, so it says nothing either way.
		if !errors.Is(err, context.Canceled) {
			t.stats.Throttled = true
		}
	case isPushbackStatus(resp.StatusCode):
		t.stats.Throttled = true
		wait := parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		t.stats.RetryAfter = max(t.stats.RetryAfter, wait)
	default:
		t.stats.Completed++
	}
}

// take returns the stats since the last call and resets them.
func (t *pushbackTransport) take() RequestStats {
	t.mu.Lock()
	defer t.mu.Unlock()
	stats := t.stats
	t.stats = RequestStats{}
	return stats
}

// isPushbackStatus reports whether a status means the server or a proxy wants
// less traffic. 403 is deliberately absent, because a missing token scope
// returns it too.
func isPushbackStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	}
	return false
}

// parseRetryAfter reads a Retry-After value, either a number of seconds or an
// HTTP-date. It returns 0 for anything absent, unusable or already past.
func parseRetryAfter(value string, now time.Time) time.Duration {
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds <= 0 || seconds > int64(math.MaxInt64/time.Second) {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	date, err := http.ParseTime(value)
	if err != nil {
		return 0
	}
	return max(date.Sub(now), 0)
}

// TakeRequestStats returns the stats for this client's requests since the last
// call and resets them, so that each reader sees every request exactly once.
func (c *Client) TakeRequestStats() RequestStats { return c.requests.take() }
