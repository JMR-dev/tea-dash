# Sub-minute auto-refresh with automatic backoff — design

**Date:** 2026-11-01
**Status:** Proposed, pending maintainer input on the field name and how to split the PR
**Area:** `internal/config`, `internal/gitea`, `internal/ui`

## Problem

tea-dash is one pane of glass for a Gitea instance: CI runs, PR checks and the inbox in one terminal
view. The fastest auto-refresh is `refetchIntervalMinutes: 1`, so a running CI job can be a minute
behind. Keeping it current means pressing `r` repeatedly or switching to the web UI, which is what
the dashboard is meant to save.

## Why lowering the interval isn't enough

| Today | Effect at 1s |
|---|---|
| A refresh sets `isLoading`, which replaces the table with a spinner and ignores keys | Table flashes; keys are dropped |
| A failed fetch replaces the table with an error screen | One 429 blanks the view |
| Each fetch overwrites `lastFetchID`, and older results are dropped | If a request takes over 1s, nothing ever displays, and requests pile up |
| `refreshAllSections` clears detail caches, and every finished fetch re-renders the preview | Preview flashes "Loading"; each tab's result can trigger its own detail requests |
| `SetTabs` always scrolls to the top | Preview can't be scrolled |
| A refresh resets to page 1 | Paged-in rows vanish and the cursor jumps |
| The cursor is an index, not a row | A new run at the top silently moves the selection, so a rerun or merge could hit the wrong row |

## Design

### Config

`defaults.refetchIntervalSeconds` is an integer, minimum 1, and additive so existing configs keep
working. Setting it together with `refetchIntervalMinutes` is a validation error. Auto-refresh stays
off by default.

### Quiet background refresh

All auto-refresh, including minute intervals, uses a new `Section.RefreshRows()`:

- It does nothing when a fetch is loading or in flight, more rows are loading, search is focused, or
  the section is past page 1. A tab past page 1 pauses rather than losing the reader's position, and
  the status line says so.
- It shows no spinner and starts no task. Rows stay on screen and keys keep working.
- A failure keeps the rows and is reported in the status line. The error screen only appears if
  nothing has ever loaded.
- The cursor follows the selected row by identity: repo + number, or repo + title for rows with no
  number.
- The preview keeps its scroll position. A tick re-fetches only the selected row's detail, and drops
  other cached details so they're fresh when selected. A failed background detail keeps the old one.

Manual `r`/`R` keeps today's visible behaviour.

### Detecting pushback

An observing `RoundTripper` wraps the HTTP client shared by the SDK and the raw helpers, so it sees
every request without editing each call site. SDK errors are plain strings, so status codes can't be
recovered from them further up. It never retries, so it can't add traffic.

- **Counts as pushback:** 429, 502, 503, 504, connection failures and timeouts.
- **Doesn't count:** `context.Canceled`, and 401, 403, 404 and 500. A missing token scope also returns
  403, so a proxy that signals rate limits with 403 won't trigger backoff.

### Backoff

Each tick reads and resets the observed counters, so one tick moves one level however many tabs
failed. The next interval is `min(base × 2^level, max(60s, base))`, so 1s goes 2, 4, 8, 16, 32, 60.
Intervals of a minute or more are unchanged.

- Pushback goes up a level.
- A clean tick with at least one completed request comes down a level.
- A tick with nothing completed stays put.
- A change takes effect from the next tick, a lag of one interval.

`Retry-After`, in seconds or as an HTTP-date, sets a minimum for the next wait. It's honored even past
60s because it's an explicit server instruction, then the normal schedule resumes. A tick that sees
it sends no requests and only reschedules. The status line shows when refresh is slowed, and manual
`r` still works.

## Cost

A tick is one request per tab in the current view, plus the preview's detail calls. This is more
traffic than today, which is why the setting is opt-in, requests don't overlap per section, and
backoff is automatic.

## Out of scope

Request retries, new key bindings, per-tab rates and new endpoints.
