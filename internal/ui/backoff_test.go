package ui

import (
	stdctx "context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/gbarany/tea-dash/internal/auth"
	"github.com/gbarany/tea-dash/internal/config"
	"github.com/gbarany/tea-dash/internal/data"
	"github.com/gbarany/tea-dash/internal/gitea"
)

var (
	pushback = gitea.RequestStats{Throttled: true}
	clean    = gitea.RequestStats{Completed: 3}
	idle     = gitea.RequestStats{}
)

func TestBackoffScheduleDoublesUpToOneMinute(t *testing.T) {
	want := []time.Duration{
		2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second,
		32 * time.Second, time.Minute, time.Minute, time.Minute,
	}
	var b refreshBackoff
	if got := b.wait(time.Second); got != time.Second {
		t.Fatalf("wait before any pushback = %v, want the configured 1s", got)
	}
	for i, expected := range want {
		b.observe(time.Second, pushback)
		if got := b.wait(time.Second); got != expected {
			t.Fatalf("after %d pushbacks: wait = %v, want %v", i+1, got, expected)
		}
	}
}

func TestBackoffScheduleFromOtherIntervals(t *testing.T) {
	tests := []struct {
		name string
		base time.Duration
		want []time.Duration
	}{
		{"just under the limit", 45 * time.Second, []time.Duration{time.Minute, time.Minute}},
		{"exactly the limit", time.Minute, []time.Duration{time.Minute, time.Minute}},
		{"above the limit stays put", 5 * time.Minute, []time.Duration{5 * time.Minute, 5 * time.Minute}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b refreshBackoff
			for i, expected := range tt.want {
				b.observe(tt.base, pushback)
				if got := b.wait(tt.base); got != expected {
					t.Fatalf("after %d pushbacks: wait = %v, want %v", i+1, got, expected)
				}
			}
		})
	}
}

func TestBackoffRecoversOneLevelPerCleanTick(t *testing.T) {
	var b refreshBackoff
	for range 3 {
		b.observe(time.Second, pushback)
	}
	if got := b.wait(time.Second); got != 8*time.Second {
		t.Fatalf("wait = %v, want 8s", got)
	}

	for _, expected := range []time.Duration{4 * time.Second, 2 * time.Second, time.Second, time.Second} {
		b.observe(time.Second, clean)
		if got := b.wait(time.Second); got != expected {
			t.Fatalf("wait = %v, want %v", got, expected)
		}
	}
}

func TestBackoffRecoveryFromTheLimitIsNotDelayedByExtraPushback(t *testing.T) {
	var b refreshBackoff
	for range 50 {
		b.observe(time.Second, pushback)
	}

	b.observe(time.Second, clean)

	if got := b.wait(time.Second); got != 32*time.Second {
		t.Fatalf("wait = %v, want one step down from the limit, 32s", got)
	}
}

func TestBackoffHoldsWhenNothingCompleted(t *testing.T) {
	var b refreshBackoff
	b.observe(time.Second, pushback)

	b.observe(time.Second, idle)

	if got := b.wait(time.Second); got != 2*time.Second {
		t.Fatalf("wait = %v, want it held at 2s: no answers says nothing about the server", got)
	}
}

func TestBackoffCountsPushbackOverCompletedRequests(t *testing.T) {
	var b refreshBackoff

	b.observe(time.Second, gitea.RequestStats{Throttled: true, Completed: 6})

	if got := b.wait(time.Second); got != 2*time.Second {
		t.Fatalf("wait = %v, want 2s when only some tabs were throttled", got)
	}
}

func TestBackoffHonorsRetryAfterPastTheLimitForOneWait(t *testing.T) {
	var b refreshBackoff

	b.observe(time.Second, gitea.RequestStats{Throttled: true, RetryAfter: 5 * time.Minute})
	if got := b.wait(time.Second); got != 5*time.Minute {
		t.Fatalf("wait = %v, want the server's 5m", got)
	}

	b.observe(time.Second, idle)
	if got := b.wait(time.Second); got != 2*time.Second {
		t.Fatalf("wait = %v, want the normal schedule to resume at 2s", got)
	}
}

func TestBackoffScheduleWinsOverAShorterRetryAfter(t *testing.T) {
	var b refreshBackoff
	for range 5 {
		b.observe(time.Second, pushback)
	}

	b.observe(time.Second, gitea.RequestStats{Throttled: true, RetryAfter: 5 * time.Second})

	if got := b.wait(time.Second); got != time.Minute {
		t.Fatalf("wait = %v, want the longer 1m", got)
	}
}

// newPushbackModel is a loaded model with a real client against a server whose
// status for repo requests can be changed by the test.
func newPushbackModel(t *testing.T, retryAfter string) (Model, *atomic.Int32, *gitea.Client) {
	t.Helper()
	status := &atomic.Int32{}
	status.Store(http.StatusOK)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/version", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"version":"1.22.0"}`)
	})
	mux.HandleFunc("/api/v1/user", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"id":1,"login":"me","full_name":"Me"}`)
	})
	mux.HandleFunc("/api/v1/repos/", func(w http.ResponseWriter, _ *http.Request) {
		code := int(status.Load())
		if code != http.StatusOK && retryAfter != "" {
			w.Header().Set("Retry-After", retryAfter)
		}
		w.WriteHeader(code)
		fmt.Fprint(w, `[]`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client, err := gitea.NewClient(stdctx.Background(), auth.Config{URL: srv.URL, Token: "t"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	cfg := &config.Config{
		Defaults: config.Defaults{RefetchIntervalMinutes: 1},
		PRSections: []config.SectionConfig{
			{Title: "Mine", Filter: config.PrIssueFilter{State: "open"}},
		},
	}
	m := NewWithOptions(cfg, client, Options{})
	m.ctx.PreviewOpen = false
	m = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = update(t, m, fetchedMsg([]data.PullRequest{{
		Number: 1, Title: "First", RepoNameWithOwner: "gitea/tea", Author: "me", State: "open",
	}}))
	client.TakeRequestStats()
	return m, status, client
}

func TestRetryAfterSkipsTheNextFetchesAndShowsInTheStatusLine(t *testing.T) {
	m, status, client := newPushbackModel(t, "120")
	status.Store(http.StatusTooManyRequests)
	if _, err := client.GetPullDetail("a", "b", 1); err == nil {
		t.Fatal("expected the request to be refused")
	}

	m = update(t, m, autoRefreshMsg{})

	if got := m.autoRefreshInterval(); got != 2*time.Minute {
		t.Fatalf("interval = %v, want the server's 2m", got)
	}
	if !strings.Contains(m.statusLine(), "refresh slowed to 2m0s (server busy)") {
		t.Fatalf("status line = %q, want it to say refresh has slowed", m.statusLine())
	}
	// The section would refuse to refresh if a fetch had started on this tick.
	if m.getCurrSection().RefreshRows() == nil {
		t.Fatal("a tick that sees Retry-After must not send requests")
	}
}

func TestStatusLineReturnsToNormalAfterRetryAfter(t *testing.T) {
	m, status, client := newPushbackModel(t, "120")
	status.Store(http.StatusTooManyRequests)
	_, _ = client.GetPullDetail("a", "b", 1)
	m = update(t, m, autoRefreshMsg{})

	m = update(t, m, autoRefreshMsg{})

	if got := m.autoRefreshInterval(); got != time.Minute {
		t.Fatalf("interval = %v, want the configured 1m", got)
	}
	if strings.Contains(m.statusLine(), "slowed") {
		t.Fatalf("status line = %q, want it back to normal", m.statusLine())
	}
}

func TestTickWithoutRetryAfterStillFetches(t *testing.T) {
	m, status, client := newPushbackModel(t, "")
	status.Store(http.StatusServiceUnavailable)
	_, _ = client.GetPullDetail("a", "b", 1)

	m = update(t, m, autoRefreshMsg{})

	if m.getCurrSection().RefreshRows() != nil {
		t.Fatal("without Retry-After the tick should fetch, on the slower schedule from then on")
	}
}

func TestManualOnlyModelIgnoresRequestStats(t *testing.T) {
	m, status, client := newPushbackModel(t, "120")
	m.ctx.Config.Defaults.RefetchIntervalMinutes = 0
	status.Store(http.StatusTooManyRequests)
	_, _ = client.GetPullDetail("a", "b", 1)

	m = update(t, m, autoRefreshMsg{})

	if got := m.autoRefreshInterval(); got != 0 {
		t.Fatalf("interval = %v, want auto-refresh to stay off", got)
	}
	if strings.Contains(m.statusLine(), "slowed") {
		t.Fatalf("status line = %q, want nothing about slowing", m.statusLine())
	}
}
