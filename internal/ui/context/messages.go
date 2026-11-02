package context

import tea "charm.land/bubbletea/v2"

// TaskFinishedMsg is the single routing envelope: an async result self-routes
// to the owning section by (SectionId, SectionType); Msg carries the payload.
type TaskFinishedMsg struct {
	SectionId   int
	SectionType string
	TaskId      string
	Msg         tea.Msg
	// Background marks the result of a quiet auto-refresh, as opposed to a
	// fetch the user can see in progress.
	Background bool
}
