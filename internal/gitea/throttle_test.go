package gitea

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gbarany/tea-dash/internal/auth"
)

// newStatusClient returns a client whose repo requests all answer with status,
// with stats from construction already drained.
func newStatusClient(t *testing.T, status int, retryAfter string) *Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/version", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"version":"1.22.0"}`)
	})
	mux.HandleFunc("/api/v1/user", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"id":1,"login":"me","full_name":"Me"}`)
	})
	mux.HandleFunc("/api/v1/repos/", func(w http.ResponseWriter, _ *http.Request) {
		if retryAfter != "" {
			w.Header().Set("Retry-After", retryAfter)
		}
		w.WriteHeader(status)
		fmt.Fprint(w, `{"message":"status from the test"}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c, err := NewClient(context.Background(), auth.Config{URL: srv.URL, Token: "t"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	c.TakeRequestStats()
	return c
}

func TestPushbackStatusesAreSeenOnSDKAndRawRequests(t *testing.T) {
	requests := []struct {
		name string
		send func(c *Client) error
	}{
		{"typed SDK call", func(c *Client) error {
			_, err := c.GetPullDetail("a", "b", 1)
			return err
		}},
		{"raw helper", func(c *Client) error {
			var out any
			_, err := c.rawGet(context.Background(), "/repos/a/b/actions/runs", &out)
			return err
		}},
	}
	statuses := []struct {
		status    int
		throttled bool
	}{
		{http.StatusTooManyRequests, true},
		{http.StatusBadGateway, true},
		{http.StatusServiceUnavailable, true},
		{http.StatusGatewayTimeout, true},
		{http.StatusUnauthorized, false},
		{http.StatusForbidden, false},
		{http.StatusNotFound, false},
		{http.StatusInternalServerError, false},
	}
	for _, request := range requests {
		for _, tt := range statuses {
			t.Run(fmt.Sprintf("%s/%d", request.name, tt.status), func(t *testing.T) {
				c := newStatusClient(t, tt.status, "")

				if err := request.send(c); err == nil {
					t.Fatalf("expected an error from status %d", tt.status)
				}

				stats := c.TakeRequestStats()
				if stats.Throttled != tt.throttled {
					t.Fatalf("Throttled = %v, want %v", stats.Throttled, tt.throttled)
				}
				if !tt.throttled && stats.Completed == 0 {
					t.Fatal("an answer that isn't pushback should count as completed")
				}
				if tt.throttled && stats.Completed != 0 {
					t.Fatalf("Completed = %d, want pushback not to count", stats.Completed)
				}
			})
		}
	}
}

func TestSuccessfulRequestCountsAsCompleted(t *testing.T) {
	c := newStatusClient(t, http.StatusOK, "")
	var out any
	if _, err := c.rawGet(context.Background(), "/repos/a/b/actions/runs", &out); err != nil {
		t.Fatalf("rawGet: %v", err)
	}

	stats := c.TakeRequestStats()

	if stats.Throttled || stats.Completed != 1 {
		t.Fatalf("stats = %+v, want one completed request and no pushback", stats)
	}
}

func TestTakeRequestStatsResets(t *testing.T) {
	c := newStatusClient(t, http.StatusTooManyRequests, "30")
	var out any
	_, _ = c.rawGet(context.Background(), "/repos/a/b/actions/runs", &out)

	if stats := c.TakeRequestStats(); !stats.Throttled {
		t.Fatal("the first read should report the pushback")
	}
	if stats := c.TakeRequestStats(); stats != (RequestStats{}) {
		t.Fatalf("second read = %+v, want empty", stats)
	}
}

func TestRetryAfterIsReportedOnPushback(t *testing.T) {
	c := newStatusClient(t, http.StatusTooManyRequests, "30")
	var out any
	_, _ = c.rawGet(context.Background(), "/repos/a/b/actions/runs", &out)

	if got := c.TakeRequestStats().RetryAfter; got != 30*time.Second {
		t.Fatalf("RetryAfter = %v, want 30s", got)
	}
}

func TestRetryAfterIsIgnoredWithoutPushback(t *testing.T) {
	c := newStatusClient(t, http.StatusOK, "30")
	var out any
	_, _ = c.rawGet(context.Background(), "/repos/a/b/actions/runs", &out)

	if got := c.TakeRequestStats().RetryAfter; got != 0 {
		t.Fatalf("RetryAfter = %v, want 0", got)
	}
}

// fixedResponse is a RoundTripper that always answers with its response or error.
type fixedResponse struct {
	response *http.Response
	err      error
}

func (f fixedResponse) RoundTrip(*http.Request) (*http.Response, error) {
	return f.response, f.err
}

func TestLongestRetryAfterWins(t *testing.T) {
	responses := []string{"10", "45", "20"}
	transport := &pushbackTransport{}
	req := httptest.NewRequest(http.MethodGet, "http://gitea.test/", nil)
	for _, value := range responses {
		transport.next = fixedResponse{response: &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     http.Header{"Retry-After": {value}},
			Body:       http.NoBody,
		}}
		if _, err := transport.RoundTrip(req); err != nil {
			t.Fatalf("RoundTrip: %v", err)
		}
	}

	if got := transport.take().RetryAfter; got != 45*time.Second {
		t.Fatalf("RetryAfter = %v, want the longest, 45s", got)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		value string
		want  time.Duration
	}{
		{"seconds", "120", 120 * time.Second},
		{"zero seconds", "0", 0},
		{"negative seconds", "-5", 0},
		{"future date", now.Add(90 * time.Second).Format(http.TimeFormat), 90 * time.Second},
		{"past date", now.Add(-90 * time.Second).Format(http.TimeFormat), 0},
		{"absent", "", 0},
		{"garbage", "soon", 0},
		{"too large for a duration", "99999999999999999999", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseRetryAfter(tt.value, now); got != tt.want {
				t.Fatalf("parseRetryAfter(%q) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}

func TestConnectionFailureCountsAsPushback(t *testing.T) {
	failure := errors.New("connection refused")
	transport := &pushbackTransport{next: fixedResponse{err: failure}}
	req := httptest.NewRequest(http.MethodGet, "http://gitea.test/", nil)

	if _, err := transport.RoundTrip(req); !errors.Is(err, failure) {
		t.Fatalf("RoundTrip error = %v, want the original error returned unchanged", err)
	}

	if stats := transport.take(); !stats.Throttled {
		t.Fatal("a connection failure should count as pushback")
	}
}

func TestCancelledRequestIsNotPushback(t *testing.T) {
	transport := &pushbackTransport{next: fixedResponse{err: fmt.Errorf("fetching: %w", context.Canceled)}}
	req := httptest.NewRequest(http.MethodGet, "http://gitea.test/", nil)

	_, _ = transport.RoundTrip(req)

	if stats := transport.take(); stats != (RequestStats{}) {
		t.Fatalf("stats = %+v, want cancellation not to count at all", stats)
	}
}

// This goes through a real http.Client timeout, to pin down the error the
// transport actually receives rather than assuming its shape.
func TestClientTimeoutCountsAsPushback(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-release
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	transport := &pushbackTransport{next: http.DefaultTransport}
	client := &http.Client{Timeout: 50 * time.Millisecond, Transport: transport}

	resp, err := client.Get(srv.URL)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("expected the request to time out")
	}

	if stats := transport.take(); !stats.Throttled {
		t.Fatalf("stats = %+v after %v, want a timeout to count as pushback", stats, err)
	}
}

func TestStatsAreSafeUnderConcurrentRequests(t *testing.T) {
	transport := &pushbackTransport{next: fixedResponse{response: &http.Response{
		StatusCode: http.StatusOK, Header: http.Header{}, Body: http.NoBody,
	}}}
	req := httptest.NewRequest(http.MethodGet, "http://gitea.test/", nil)
	const requests = 50

	var wg sync.WaitGroup
	completed := make(chan int, requests)
	for range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = transport.RoundTrip(req)
			completed <- transport.take().Completed
		}()
	}
	wg.Wait()
	close(completed)

	total := transport.take().Completed
	for n := range completed {
		total += n
	}
	if total != requests {
		t.Fatalf("counted %d completed requests, want %d", total, requests)
	}
}
