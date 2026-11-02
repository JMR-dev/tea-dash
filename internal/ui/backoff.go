package ui

import (
	"time"

	"github.com/gbarany/tea-dash/internal/gitea"
)

// backoffLimit is the slowest the schedule goes on its own. Configured intervals
// longer than this are left alone, and an explicit Retry-After can exceed it.
const backoffLimit = time.Minute

// refreshBackoff slows auto-refresh while the server is pushing back. It's fed
// once per tick, so one tick moves one level however many requests failed.
type refreshBackoff struct {
	level      int
	retryAfter time.Duration
}

// observe updates the schedule from the requests finished since the last tick.
// Pushback slows it by a level, a clean tick speeds it up by one, and a tick in
// which nothing finished says nothing about the server, so it changes nothing.
func (b *refreshBackoff) observe(base time.Duration, stats gitea.RequestStats) {
	b.retryAfter = stats.RetryAfter
	switch {
	case stats.Throttled:
		// Stop counting at the limit, so that recovery starts as soon as the
		// server does however long the pushback lasted.
		if b.scheduled(base) < limitFor(base) {
			b.level++
		}
	case stats.Completed > 0 && b.level > 0:
		b.level--
	}
}

// wait returns how long to wait before the next tick: the schedule, or the
// server's Retry-After if that's longer.
func (b refreshBackoff) wait(base time.Duration) time.Duration {
	return max(b.scheduled(base), b.retryAfter)
}

func (b refreshBackoff) scheduled(base time.Duration) time.Duration {
	return min(base<<b.level, limitFor(base))
}

func limitFor(base time.Duration) time.Duration {
	return max(backoffLimit, base)
}
