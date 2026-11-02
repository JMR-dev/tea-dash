package section

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/gbarany/tea-dash/internal/data"
	localgit "github.com/gbarany/tea-dash/internal/git"
)

// moveCursorDown presses the down key n times.
func (f *refreshFixture) moveCursorDown(t *testing.T, n int) {
	t.Helper()
	for range n {
		next, _ := f.model.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		f.model = next.(*Model[data.PullRequest])
	}
}

func TestBackgroundRefreshKeepsSelectionWhenARowArrivesOnTop(t *testing.T) {
	f := newRefreshFixture(t)
	f.loadRows(t, pullRow(2, "second"), pullRow(1, "first"))
	f.moveCursorDown(t, 1)
	if got := f.model.GetCurrRow().GetNumber(); got != 1 {
		t.Fatalf("selected #%d before the refresh, want #1", got)
	}

	f.rows, f.total = []data.PullRequest{pullRow(3, "third"), pullRow(2, "second"), pullRow(1, "first")}, 3
	f.deliver(t, f.model.RefreshRows())

	if got := f.model.GetCurrRow().GetNumber(); got != 1 {
		t.Fatalf("selected #%d after the refresh, want it to stay on #1", got)
	}
	if got := f.model.CurrRow(); got != 2 {
		t.Fatalf("cursor = %d, want 2", got)
	}
}

func TestBackgroundRefreshClampsWhenTheSelectedRowVanishes(t *testing.T) {
	f := newRefreshFixture(t)
	f.loadRows(t, pullRow(3, "third"), pullRow(2, "second"), pullRow(1, "first"))
	f.moveCursorDown(t, 2)

	f.rows, f.total = []data.PullRequest{pullRow(3, "third")}, 1
	f.deliver(t, f.model.RefreshRows())

	if got := f.model.CurrRow(); got != 0 {
		t.Fatalf("cursor = %d, want clamped to 0", got)
	}
	row := f.model.GetCurrRow()
	if row == nil || row.GetNumber() != 3 {
		t.Fatalf("selected row = %v, want #3", row)
	}
}

func TestBackgroundRefreshToEmptyThenBackDoesNotStrandTheCursor(t *testing.T) {
	f := newRefreshFixture(t)
	f.loadRows(t, pullRow(1, "first"))

	f.rows, f.total = nil, 0
	f.deliver(t, f.model.RefreshRows())
	if f.model.GetCurrRow() != nil {
		t.Fatal("an empty section should have no selected row")
	}

	f.rows, f.total = []data.PullRequest{pullRow(2, "second")}, 1
	f.deliver(t, f.model.RefreshRows())

	row := f.model.GetCurrRow()
	if row == nil || row.GetNumber() != 2 {
		t.Fatalf("selected row = %v, want #2", row)
	}
}

func TestForegroundFetchKeepsCursorIndex(t *testing.T) {
	f := newRefreshFixture(t)
	f.loadRows(t, pullRow(2, "second"), pullRow(1, "first"))
	f.moveCursorDown(t, 1)

	f.rows, f.total = []data.PullRequest{pullRow(3, "third"), pullRow(2, "second"), pullRow(1, "first")}, 3
	f.deliver(t, f.model.FetchRows())

	if got := f.model.CurrRow(); got != 1 {
		t.Fatalf("cursor = %d, want manual refresh to keep the index", got)
	}
}

func TestRowIdentity(t *testing.T) {
	tests := []struct {
		name      string
		row, same data.RowData
		different data.RowData
	}{
		{
			"pull requests use repo and number",
			data.PullRequest{Number: 7, Title: "old title", RepoNameWithOwner: "a/b"},
			data.PullRequest{Number: 7, Title: "new title", RepoNameWithOwner: "a/b"},
			data.PullRequest{Number: 7, Title: "old title", RepoNameWithOwner: "a/c"},
		},
		{
			"issues use repo and number",
			data.Issue{Number: 7, Title: "old title", RepoNameWithOwner: "a/b"},
			data.Issue{Number: 7, Title: "new title", RepoNameWithOwner: "a/b"},
			data.Issue{Number: 8, Title: "old title", RepoNameWithOwner: "a/b"},
		},
		{
			"action runs use repo and number",
			data.ActionRun{RunNumber: 7, RepoNameWithOwner: "a/b"},
			data.ActionRun{RunNumber: 7, RepoNameWithOwner: "a/b"},
			data.ActionRun{RunNumber: 8, RepoNameWithOwner: "a/b"},
		},
		{
			"notifications without a number use the title",
			data.Notification{SubjectTitle: "release", RepoNameWithOwner: "a/b"},
			data.Notification{SubjectTitle: "release", RepoNameWithOwner: "a/b"},
			data.Notification{SubjectTitle: "other", RepoNameWithOwner: "a/b"},
		},
		{
			"branches have no number and use the name",
			localgit.Branch{Name: "main", Repository: "a/b"},
			localgit.Branch{Name: "main", Repository: "a/b"},
			localgit.Branch{Name: "dev", Repository: "a/b"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if rowIdentity(tt.row) != rowIdentity(tt.same) {
				t.Fatalf("%q != %q, want the same identity", rowIdentity(tt.row), rowIdentity(tt.same))
			}
			if rowIdentity(tt.row) == rowIdentity(tt.different) {
				t.Fatalf("both rows have identity %q, want different", rowIdentity(tt.row))
			}
		})
	}
}
