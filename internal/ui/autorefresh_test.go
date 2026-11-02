package ui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/gbarany/tea-dash/internal/config"
	"github.com/gbarany/tea-dash/internal/data"
	"github.com/gbarany/tea-dash/internal/ui/components/pullsection"
	"github.com/gbarany/tea-dash/internal/ui/context"
)

// newAutoRefreshModel is a model on the demo server, with a real client, an open
// preview and auto-refresh turned on.
func newAutoRefreshModel(t *testing.T) Model {
	t.Helper()
	m, _ := newE2EModel(t)
	m.ctx.Config.Defaults.RefetchIntervalMinutes = 1
	m.ctx.PreviewOpen = true
	if m.selKey() == "" {
		t.Fatal("the demo data should give the first section a selected row")
	}
	return m
}

func tick(t *testing.T, m Model) Model {
	t.Helper()
	next, cmd := m.Update(autoRefreshMsg{})
	if cmd == nil {
		t.Fatal("a tick should always reschedule")
	}
	return next.(Model)
}

func TestAutoRefreshTickKeepsSelectedDetailAndDropsOthers(t *testing.T) {
	m := newAutoRefreshModel(t)
	key := m.selKey()
	selected := &data.PullDetail{Body: "selected"}
	m.pullDetails[key] = selected
	m.pullDetails["some/other#1"] = &data.PullDetail{Body: "other"}
	m.issueDetails["some/other#2"] = &data.IssueDetail{}
	m.actionDetails["some/other#3"] = &data.ActionRunDetail{}

	m = tick(t, m)

	if m.pullDetails[key] != selected {
		t.Fatal("the selected row's detail should stay on screen until a new one arrives")
	}
	if len(m.pullDetails) != 1 || len(m.issueDetails) != 0 || len(m.actionDetails) != 0 {
		t.Fatalf("kept %d pull, %d issue and %d action details, want only the selected pull",
			len(m.pullDetails), len(m.issueDetails), len(m.actionDetails))
	}
}

func TestAutoRefreshTickWithPreviewClosedDropsAllDetails(t *testing.T) {
	m := newAutoRefreshModel(t)
	m.ctx.PreviewOpen = false
	m.pullDetails[m.selKey()] = &data.PullDetail{Body: "selected"}

	m = tick(t, m)

	if len(m.pullDetails) != 0 {
		t.Fatalf("kept %d details, want none: nothing is showing them", len(m.pullDetails))
	}
	if m.detailRefreshPending {
		t.Fatal("a closed preview shouldn't fetch detail")
	}
}

func TestAutoRefreshFetchesSelectedDetailOnlyOnceAtATime(t *testing.T) {
	m := newAutoRefreshModel(t)
	m.pullDetails[m.selKey()] = &data.PullDetail{Body: "selected"}

	m = tick(t, m)
	if !m.detailRefreshPending {
		t.Fatal("a tick should start a detail refresh for the selected row")
	}

	if cmd := m.refreshSelectedDetail(); cmd != nil {
		t.Fatal("a second detail refresh must not start while one is in flight")
	}
}

func TestBackgroundDetailReplacesTheCachedOne(t *testing.T) {
	m := newAutoRefreshModel(t)
	key := m.selKey()
	old := &data.PullDetail{Body: "old"}
	m.pullDetails[key] = old

	cmd := m.refreshSelectedDetail()
	if cmd == nil {
		t.Fatal("expected a detail refresh")
	}
	msg, ok := cmd().(enrichedMsg)
	if !ok || !msg.background {
		t.Fatalf("detail refresh returned %#v, want a background enrichedMsg", msg)
	}
	m = update(t, m, msg)

	if m.pullDetails[key] == old {
		t.Fatal("the new detail should replace the cached one")
	}
	if m.detailRefreshPending {
		t.Fatal("the in-flight flag should clear when the result arrives")
	}
}

func TestFailedBackgroundDetailKeepsTheOldOne(t *testing.T) {
	m := newAutoRefreshModel(t)
	key := m.selKey()
	old := &data.PullDetail{Body: "old"}
	m.pullDetails[key] = old
	m.detailRefreshPending = true

	m = update(t, m, enrichedMsg{
		key: key, sectionType: pullsection.SectionType, err: errors.New("429"), background: true,
	})

	if m.pullDetails[key] != old {
		t.Fatal("a failed background detail must keep the old one")
	}
	if m.pullEnrichErr[key] != nil {
		t.Fatalf("pullEnrichErr = %v, want nil so the preview isn't replaced by an error", m.pullEnrichErr[key])
	}
	if m.detailRefreshPending {
		t.Fatal("the in-flight flag should clear on failure too")
	}
}

func TestFailedBackgroundDetailWithNothingCachedShowsTheError(t *testing.T) {
	m := newAutoRefreshModel(t)
	key := m.selKey()
	delete(m.pullDetails, key)

	m = update(t, m, enrichedMsg{
		key: key, sectionType: pullsection.SectionType, err: errors.New("401"), background: true,
	})

	if m.pullEnrichErr[key] == nil {
		t.Fatal("with no detail to keep, the failure should be shown")
	}
}

// scrolledPreviewModel has a selected row with a preview long enough to scroll,
// scrolled away from the top.
func scrolledPreviewModel(t *testing.T) Model {
	t.Helper()
	m := newAutoRefreshModel(t)
	m = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 16})
	m.pullDetails[m.selKey()] = &data.PullDetail{Body: strings.Repeat("a line of text\n\n", 200)}
	m.expanded = true
	m.syncSidebar()
	next, _, _ := m.sidebar.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	m.sidebar = next
	if m.sidebar.ScrollOffset() == 0 {
		t.Fatal("the preview should be long enough to scroll")
	}
	return m
}

func TestBackgroundDetailKeepsPreviewScroll(t *testing.T) {
	m := scrolledPreviewModel(t)
	offset := m.sidebar.ScrollOffset()

	m = update(t, m, enrichedMsg{
		key: m.selKey(), sectionType: pullsection.SectionType, background: true,
		pull: &data.PullDetail{Body: strings.Repeat("a line of text\n\n", 201)},
	})

	if got := m.sidebar.ScrollOffset(); got != offset {
		t.Fatalf("ScrollOffset = %d, want %d", got, offset)
	}
}

func TestBackgroundRowsKeepPreviewScroll(t *testing.T) {
	m := scrolledPreviewModel(t)
	offset := m.sidebar.ScrollOffset()
	row := m.getCurrRowData().(data.PullRequest)

	m = update(t, m, context.TaskFinishedMsg{
		SectionId: m.getCurrSection().GetId(), SectionType: pullsection.SectionType, Background: true,
		Msg: pullsection.SectionPullRequestsFetchedMsg{
			Rows: []data.PullRequest{row}, TotalCount: 1, Page: 1, Background: true,
			TaskId: m.getCurrSection().(interface{ LastFetchID() string }).LastFetchID(),
		},
	})

	if got := m.sidebar.ScrollOffset(); got != offset {
		t.Fatalf("ScrollOffset = %d, want %d", got, offset)
	}
}

func TestBackgroundRowsResetScrollWhenTheSelectedRowChanged(t *testing.T) {
	m := scrolledPreviewModel(t)
	replacement := data.PullRequest{Number: 99999, Title: "New", RepoNameWithOwner: "teahouse/other", State: "open"}

	m = update(t, m, context.TaskFinishedMsg{
		SectionId: m.getCurrSection().GetId(), SectionType: pullsection.SectionType, Background: true,
		Msg: pullsection.SectionPullRequestsFetchedMsg{
			Rows: []data.PullRequest{replacement}, TotalCount: 1, Page: 1, Background: true,
			TaskId: m.getCurrSection().(interface{ LastFetchID() string }).LastFetchID(),
		},
	})

	if got := m.sidebar.ScrollOffset(); got != 0 {
		t.Fatalf("ScrollOffset = %d, want 0 for a different row", got)
	}
}

func TestBackgroundResultsFromOtherTabsLeaveThePreviewAlone(t *testing.T) {
	cfg := &config.Config{
		Defaults: config.Defaults{RefetchIntervalMinutes: 1},
		PRSections: []config.SectionConfig{
			{Title: "A", Filter: config.PrIssueFilter{State: "open"}},
			{Title: "B", Filter: config.PrIssueFilter{State: "open"}},
		},
	}
	m := New(cfg, nil)
	m = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m.ctx.PreviewOpen = true
	m.sidebar.SetContent("sentinel")

	next, cmd := m.Update(context.TaskFinishedMsg{
		SectionId: 1, SectionType: pullsection.SectionType, Background: true,
		Msg: pullsection.SectionPullRequestsFetchedMsg{
			Rows:       []data.PullRequest{{Number: 1, Title: "x", RepoNameWithOwner: "a/b"}},
			TotalCount: 1, Page: 1, Background: true,
		},
	})
	m = next.(Model)

	if cmd != nil {
		t.Fatal("a background result from another tab shouldn't start any requests")
	}
	if !strings.Contains(m.sidebar.View(), "sentinel") {
		t.Fatal("a background result from another tab shouldn't re-render the preview")
	}
}

func TestBackgroundRowsDoNotRefetchCachedDetail(t *testing.T) {
	m := newAutoRefreshModel(t)
	key := m.selKey()
	row := m.getCurrRowData().(data.PullRequest)
	m.pullDetails[key] = &data.PullDetail{Body: "cached"}

	next, cmd := m.Update(context.TaskFinishedMsg{
		SectionId: m.getCurrSection().GetId(), SectionType: pullsection.SectionType, Background: true,
		Msg: pullsection.SectionPullRequestsFetchedMsg{
			Rows: []data.PullRequest{row}, TotalCount: 1, Page: 1, Background: true,
			TaskId: m.getCurrSection().(interface{ LastFetchID() string }).LastFetchID(),
		},
	})
	m = next.(Model)

	if cmd != nil {
		t.Fatal("with the selected detail cached, a background result shouldn't start any requests")
	}
	if m.detailRefreshPending {
		t.Fatal("no detail refresh should be in flight")
	}
}

func TestManualRefreshStillClearsDetails(t *testing.T) {
	m := newAutoRefreshModel(t)
	m.pullDetails[m.selKey()] = &data.PullDetail{Body: "selected"}

	_ = m.refreshAllSections()

	if len(m.pullDetails) != 0 {
		t.Fatalf("kept %d details, want a manual refresh to start fresh", len(m.pullDetails))
	}
}
