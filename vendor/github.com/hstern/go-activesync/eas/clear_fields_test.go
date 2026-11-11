// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"testing"
	"time"
)

// TestBuildEventAppEmitsEmptyClearedFields guards the clear mechanism: a
// cleared field must be serialized as an explicit empty element, because
// omitting it would leave the server's stored value intact (Exchange
// replaces the whole item on a Change).
func TestBuildEventAppEmitsEmptyClearedFields(t *testing.T) {
	draft := EventDraft{
		Subject:     "keep",
		StartTime:   time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC),
		EndTime:     time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC),
		ClearFields: []string{"location", "attendees", "reminder"},
	}
	app := buildEventApp(draft)

	loc := app.Find("Location")
	if loc == nil {
		t.Fatal("no Location element for a cleared location")
	}
	if got := loc.TextContent(); got != "" {
		t.Errorf("cleared Location = %q, want empty", got)
	}

	atts := app.Find("Attendees")
	if atts == nil {
		t.Fatal("no Attendees element for a cleared attendee list")
	}
	if len(atts.Children) != 0 {
		t.Errorf("cleared Attendees has %d children, want 0", len(atts.Children))
	}

	rem := app.Find("Reminder")
	if rem == nil {
		t.Fatal("no Reminder element for a cleared reminder")
	}
	if got := rem.TextContent(); got != "0" {
		t.Errorf("cleared Reminder = %q, want \"0\"", got)
	}

	// Control: a non-cleared field is still serialized normally.
	if got := app.Find("Subject").TextContent(); got != "keep" {
		t.Errorf("Subject = %q, want keep", got)
	}
}
