package ui

import (
	"context"

	"rave.page/mate/internal/mediaeditor"
)

// editorSource backs the media editor's "load from event" feature. The rave.page
// events/lineup endpoints aren't in the generated client yet (see mediaeditor
// INTEGRATION.md), so this returns no events for now - manual composition works.
type editorSource struct{}

func (editorSource) UpcomingEvents(context.Context) ([]mediaeditor.EventData, error) {
	return nil, nil // TODO: wire /events?filter=upcoming + /events/{id}/lineup
}
