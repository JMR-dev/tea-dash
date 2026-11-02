package section

import (
	stdctx "context"
	"errors"
	"testing"

	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"

	"github.com/gbarany/tea-dash/internal/config"
	"github.com/gbarany/tea-dash/internal/data"
	"github.com/gbarany/tea-dash/internal/gitea"
	appctx "github.com/gbarany/tea-dash/internal/ui/context"
)

// refreshFixture is a section whose fetch result is set per test.
type refreshFixture struct {
	model *Model[data.PullRequest]
	rows  []data.PullRequest
	total int
	err   error
	calls int
	tasks int
}

func pullRow(number int64, title string) data.PullRequest {
	return data.PullRequest{Number: number, Title: title, RepoNameWithOwner: "gbarany/tea-dash"}
}

func newRefreshFixture(t *testing.T) *refreshFixture {
	t.Helper()
	f := &refreshFixture{}
	ctx := &appctx.ProgramContext{
		Styles:           contextStyles(t),
		MainContentWidth: 100, MainContentHeight: 20,
		Config: &config.Config{Defaults: config.Defaults{PRsLimit: 1}},
		StartTask: func(appctx.Task) tea.Cmd {
			f.tasks++
			return nil
		},
	}
	f.model = New(Options[data.PullRequest]{
		Type:         "pr",
		FilterKind:   "pulls",
		Ctx:          ctx,
		Config:       config.SectionConfig{Title: "PRs"},
		LoadingText:  "Loading pull requests…",
		EmptyText:    "No pull requests.",
		EmptyHint:    "Try another filter.",
		SingularForm: "pull request",
		PluralForm:   "pull requests",
		Limit:        func(c *config.Config) int { return c.Defaults.PRsLimit },
		Pageable:     true,
		Fetch: func(stdctx.Context, *gitea.Client, config.PrIssueFilter, int, int) ([]data.PullRequest, int, error) {
			f.calls++
			return f.rows, f.total, f.err
		},
		BuildRow: func(pr data.PullRequest) table.Row {
			return table.Row{pr.GetTitle()}
		},
	})
	return f
}

// loadRows applies a foreground result, as the first fetch would.
func (f *refreshFixture) loadRows(t *testing.T, rows ...data.PullRequest) {
	t.Helper()
	f.model.SetLastFetchID("load")
	next, _ := f.model.Update(RowsFetchedMsg[data.PullRequest]{
		Rows: rows, TotalCount: len(rows), TaskId: "load", Page: 1,
	})
	f.model = next.(*Model[data.PullRequest])
}

// deliver runs a fetch command and applies its result to the section.
func (f *refreshFixture) deliver(t *testing.T, cmd tea.Cmd) appctx.TaskFinishedMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a fetch command")
	}
	finished, ok := runFetchCommand(t, cmd).(appctx.TaskFinishedMsg)
	if !ok {
		t.Fatal("fetch command did not return a TaskFinishedMsg")
	}
	next, _ := f.model.Update(finished.Msg)
	f.model = next.(*Model[data.PullRequest])
	return finished
}

func TestRefreshRowsKeepsTableOnScreenAndStartsNoTask(t *testing.T) {
	f := newRefreshFixture(t)
	f.loadRows(t, pullRow(1, "first"))
	tableBefore := f.model.View()

	cmd := f.model.RefreshRows()

	if f.model.GetIsLoading() {
		t.Fatal("a background refresh must not enter the loading state")
	}
	if got := f.model.View(); got != tableBefore {
		t.Fatalf("view changed while refreshing:\n%s", got)
	}
	if f.tasks != 0 {
		t.Fatalf("started %d tasks, want none", f.tasks)
	}

	f.rows, f.total = []data.PullRequest{pullRow(1, "first"), pullRow(2, "second")}, 2
	finished := f.deliver(t, cmd)

	if !finished.Background {
		t.Fatal("TaskFinishedMsg.Background = false, want true")
	}
	if got := f.model.NumRows(); got != 2 {
		t.Fatalf("NumRows = %d, want 2", got)
	}
}

func TestForegroundFetchIsNotMarkedBackground(t *testing.T) {
	f := newRefreshFixture(t)
	f.loadRows(t, pullRow(1, "first"))

	finished := f.deliver(t, f.model.FetchRows())

	if finished.Background {
		t.Fatal("TaskFinishedMsg.Background = true for a foreground fetch")
	}
	if f.tasks != 1 {
		t.Fatalf("started %d tasks, want 1", f.tasks)
	}
}

func TestRefreshFailureKeepsRowsAndKeysAndClearsOnSuccess(t *testing.T) {
	f := newRefreshFixture(t)
	f.loadRows(t, pullRow(1, "first"), pullRow(2, "second"))

	f.err = errors.New("429 Too Many Requests")
	f.deliver(t, f.model.RefreshRows())

	if err := f.model.GetError(); err != nil {
		t.Fatalf("GetError = %v, want nil so the table stays visible", err)
	}
	if f.model.GetRefreshError() == nil {
		t.Fatal("GetRefreshError = nil, want the failure recorded")
	}
	if got := f.model.NumRows(); got != 2 {
		t.Fatalf("NumRows = %d, want the last good 2", got)
	}
	next, _ := f.model.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	f.model = next.(*Model[data.PullRequest])
	if got := f.model.CurrRow(); got != 1 {
		t.Fatalf("cursor = %d, want 1: keys must keep working after a failed refresh", got)
	}

	f.err = nil
	f.rows, f.total = []data.PullRequest{pullRow(1, "first"), pullRow(2, "second")}, 2
	f.deliver(t, f.model.RefreshRows())

	if err := f.model.GetRefreshError(); err != nil {
		t.Fatalf("GetRefreshError = %v, want cleared by a successful refresh", err)
	}
}

func TestRefreshFailureBeforeAnyLoadShowsTheErrorScreen(t *testing.T) {
	f := newRefreshFixture(t)
	f.err = errors.New("401 Unauthorized")
	f.deliver(t, f.model.FetchRows())
	if f.model.GetError() == nil {
		t.Fatal("the first fetch should have set an error")
	}

	f.deliver(t, f.model.RefreshRows())

	if f.model.GetError() == nil {
		t.Fatal("nothing has ever loaded, so the error screen must stay")
	}
}

func TestRefreshSuccessClearsAnEarlierErrorScreen(t *testing.T) {
	f := newRefreshFixture(t)
	f.err = errors.New("502 Bad Gateway")
	f.deliver(t, f.model.FetchRows())

	f.err = nil
	f.rows, f.total = []data.PullRequest{pullRow(1, "first")}, 1
	f.deliver(t, f.model.RefreshRows())

	if err := f.model.GetError(); err != nil {
		t.Fatalf("GetError = %v, want cleared once a refresh succeeds", err)
	}
	if got := f.model.NumRows(); got != 1 {
		t.Fatalf("NumRows = %d, want 1", got)
	}
}

func TestRefreshRowsDoesNothingWhenItWouldInterfere(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, f *refreshFixture)
	}{
		{"first load still running", func(*testing.T, *refreshFixture) {}},
		{"foreground fetch in flight", func(t *testing.T, f *refreshFixture) {
			f.loadRows(t, pullRow(1, "first"))
			_ = f.model.FetchRows()
		}},
		{"background refresh in flight", func(t *testing.T, f *refreshFixture) {
			f.loadRows(t, pullRow(1, "first"))
			if f.model.RefreshRows() == nil {
				t.Fatal("the first refresh should start")
			}
		}},
		{"next page loading", func(t *testing.T, f *refreshFixture) {
			f.loadRows(t, pullRow(1, "first"))
			_ = f.model.fetchPage(2, true)
		}},
		{"search focused", func(t *testing.T, f *refreshFixture) {
			f.loadRows(t, pullRow(1, "first"))
			f.model.SetIsSearching(true)
		}},
		{"past page one", func(t *testing.T, f *refreshFixture) {
			f.loadRows(t, pullRow(1, "first"))
			f.model.SetLastFetchID("page2")
			next, _ := f.model.Update(RowsFetchedMsg[data.PullRequest]{
				Rows: []data.PullRequest{pullRow(2, "second")}, TotalCount: 2,
				TaskId: "page2", Page: 2, Append: true,
			})
			f.model = next.(*Model[data.PullRequest])
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newRefreshFixture(t)
			tt.setup(t, f)
			if cmd := f.model.RefreshRows(); cmd != nil {
				t.Fatal("RefreshRows returned a command, want nil")
			}
		})
	}
}

func TestAutoRefreshPausedOnlyPastPageOne(t *testing.T) {
	f := newRefreshFixture(t)
	f.loadRows(t, pullRow(1, "first"))
	if f.model.AutoRefreshPaused() {
		t.Fatal("page one must not report a pause")
	}

	f.model.SetLastFetchID("page2")
	next, _ := f.model.Update(RowsFetchedMsg[data.PullRequest]{
		Rows: []data.PullRequest{pullRow(2, "second")}, TotalCount: 2,
		TaskId: "page2", Page: 2, Append: true,
	})
	f.model = next.(*Model[data.PullRequest])
	if !f.model.AutoRefreshPaused() {
		t.Fatal("past page one must report a pause")
	}

	f.rows, f.total = []data.PullRequest{pullRow(1, "first")}, 2
	f.deliver(t, f.model.FetchRows())
	if f.model.AutoRefreshPaused() {
		t.Fatal("a manual refresh returns to page one, which resumes auto-refresh")
	}
}

func TestSupersededRefreshIsDroppedAndCannotBlockLaterRefreshes(t *testing.T) {
	f := newRefreshFixture(t)
	f.loadRows(t, pullRow(1, "first"))

	f.rows, f.total = []data.PullRequest{pullRow(9, "stale")}, 1
	stale := f.model.RefreshRows()
	staleMsg := runFetchCommand(t, stale).(appctx.TaskFinishedMsg)

	f.rows, f.total = []data.PullRequest{pullRow(1, "first"), pullRow(2, "second")}, 2
	f.deliver(t, f.model.FetchRows())

	next, _ := f.model.Update(staleMsg.Msg)
	f.model = next.(*Model[data.PullRequest])
	if got := f.model.NumRows(); got != 2 {
		t.Fatalf("NumRows = %d, want 2: the superseded refresh must be dropped", got)
	}
	if f.model.RefreshRows() == nil {
		t.Fatal("a superseded refresh left the in-flight flag stuck")
	}
}

func TestPageFetchSupersedingARefreshCannotBlockLaterRefreshes(t *testing.T) {
	f := newRefreshFixture(t)
	f.loadRows(t, pullRow(1, "first"))
	_ = f.model.RefreshRows()

	// A foreground fetch replaces the refresh's ID and clears its flag. When
	// that fetch finishes, the next refresh must be free to start.
	f.rows, f.total = []data.PullRequest{pullRow(1, "first")}, 1
	f.deliver(t, f.model.FetchRows())

	if f.model.RefreshRows() == nil {
		t.Fatal("the in-flight flag was left set after a superseding fetch")
	}
}
