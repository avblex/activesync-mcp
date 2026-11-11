# Calendar Field Clearing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let `calendar_update_event` explicitly reset event fields (location, attendees, reminder, sensitivity, busy_status) to their defaults, despite the read-modify-write merge treating a zero value as "leave unchanged".

**Architecture:** Two layers. (1) In the vendored EAS client, add `EventDraft.ClearFields []string`; `mergeEventDraft` zeroes the listed fields and carries the list through, and `buildEventApp` emits an *explicitly empty* element for the conditionally-serialized fields (location, attendees, reminder) so the server actually drops the stored value instead of keeping it. (2) In the MCP tool, add a `clear []string` input, validate it (unknown names rejected; a field supplied both as a value and in `clear` is an error), and pass it into the draft.

**Tech Stack:** Go 1.26, vendored `github.com/hstern/go-activesync v1.1.0` (edited under `vendor/`), `modelcontextprotocol/go-sdk`, table-driven + httptest/easmock unit tests.

---

## Context for the implementer

- The repo vendors its dependencies. Edits go directly into files under
  `vendor/github.com/hstern/go-activesync/eas/`. This is intentional and
  approved — the fix lives in the vendored copy.
- **Build/test quirk:** `go build`, `go vet`, and `go test` FAIL inside the
  default Bash sandbox with `cannot find module providing package ... import
  lookup disabled by -mod=vendor`. Run every `go build`/`go vet`/`go test`
  command with `dangerouslyDisableSandbox: true`. `gofmt` works fine in-sandbox.
- The read-modify-write `UpdateEvent` was added recently: it fetches the
  event's current state, merges the caller's non-zero fields on top, and sends
  a full `Change`. See `mergeEventDraft` and `buildEventApp` in
  `vendor/github.com/hstern/go-activesync/eas/calendar.go`.
- Why an explicit empty element is required: `buildEventApp` only serializes
  non-zero values (`if draft.Location != ""`, `if len(draft.Attendees) > 0`,
  `if draft.Reminder > 0`). A field merely zeroed in the merge would be
  *omitted* from the `<ApplicationData>`, and Exchange (which replaces the
  whole item on a Change) would keep the old value. So clearing needs both:
  zero the value **and** force the element to be emitted empty.
- `Sensitivity` and `BusyStatus` are *always* serialized by `buildEventApp`,
  so zeroing them in the merge is sufficient — no `buildEventApp` change needed
  for those two. Only `location`, `attendees`, `reminder` need the
  explicit-empty treatment.

### Files

- Modify: `vendor/github.com/hstern/go-activesync/eas/calendar.go`
  - `EventDraft` struct (add `ClearFields`)
  - `mergeEventDraft` (apply clears, carry the list)
  - `buildEventApp` (emit explicit-empty elements)
  - new helper method `(d EventDraft) clears(name string) bool`
- Create: `vendor/github.com/hstern/go-activesync/eas/clear_fields_test.go`
- Modify: `lib/server/tools_calendar.go`
  - `CalendarUpdateEventInput` (add `Clear`)
  - `registerCalendarUpdate` handler (validate + wire)
  - new `validateClearFields` + `clearableCalendarFields`
- Modify: `lib/server/tools_calendar_test.go` (three new tests)

---

### Task 1: `buildEventApp` emits explicit-empty elements for cleared fields

**Files:**
- Modify: `vendor/github.com/hstern/go-activesync/eas/calendar.go`
- Test: `vendor/github.com/hstern/go-activesync/eas/clear_fields_test.go`

- [ ] **Step 1: Write the failing test**

Create `vendor/github.com/hstern/go-activesync/eas/clear_fields_test.go`:

```go
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./vendor/github.com/hstern/go-activesync/eas/ -run TestBuildEventAppEmitsEmptyClearedFields -v` (with `dangerouslyDisableSandbox: true`)
Expected: compile error — `unknown field ClearFields in struct literal` (the field does not exist yet).

- [ ] **Step 3: Add the `ClearFields` field to `EventDraft`**

In `vendor/github.com/hstern/go-activesync/eas/calendar.go`, in the `EventDraft` struct, add after the `TimeZoneRaw string` field (the last field, just before the closing `}`):

```go
	// ClearFields lists field names to reset to their default on update:
	// "location", "attendees", "reminder", "sensitivity", "busy_status".
	// Because a zero value means "leave unchanged" in the read-modify-write
	// merge, this is the only way to explicitly clear a field.
	ClearFields []string
```

- [ ] **Step 4: Add the `clears` helper method**

In the same file, immediately before `func buildEventApp(`, add:

```go
// clears reports whether the draft asks to reset the named field to its
// default. buildEventApp uses it to force an explicit empty element for the
// conditionally-serialized fields (location, attendees, reminder), which
// would otherwise be omitted and left unchanged on the server.
func (d EventDraft) clears(name string) bool {
	for _, n := range d.ClearFields {
		if n == name {
			return true
		}
	}
	return false
}
```

- [ ] **Step 5: Make `buildEventApp` emit explicit-empty elements**

In `buildEventApp`, change the three conditional blocks so each also fires when the field is cleared.

Attendees block — change the condition from
`if len(draft.Attendees) > 0 {` to
`if len(draft.Attendees) > 0 || draft.clears("attendees") {`
(the inner loop adds nothing when the list is empty, yielding an empty
`<Attendees/>`).

Location block — replace:
```go
	// Location (0x17)
	if draft.Location != "" {
		app.Children = append(app.Children, wbxml.E(wbxml.PageCalendar, "Location", wbxml.Text(draft.Location)))
	}
```
with:
```go
	// Location (0x17). A cleared location is sent as an empty element so the
	// server drops the stored value; omitting it would keep the old one.
	if draft.Location != "" || draft.clears("location") {
		app.Children = append(app.Children, wbxml.E(wbxml.PageCalendar, "Location", wbxml.Text(draft.Location)))
	}
```

Reminder block — replace:
```go
	// Reminder (0x24), Sensitivity (0x25)
	if draft.Reminder > 0 {
		app.Children = append(app.Children, wbxml.E(wbxml.PageCalendar, "Reminder", wbxml.Text(itoa(draft.Reminder))))
	}
```
with:
```go
	// Reminder (0x24), Sensitivity (0x25). A cleared reminder is sent as an
	// explicit 0 so the server drops the stored reminder.
	if draft.Reminder > 0 || draft.clears("reminder") {
		app.Children = append(app.Children, wbxml.E(wbxml.PageCalendar, "Reminder", wbxml.Text(itoa(draft.Reminder))))
	}
```

- [ ] **Step 6: Run the test to verify it passes**

Run: `go test ./vendor/github.com/hstern/go-activesync/eas/ -run TestBuildEventAppEmitsEmptyClearedFields -v` (with `dangerouslyDisableSandbox: true`)
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add vendor/github.com/hstern/go-activesync/eas/calendar.go vendor/github.com/hstern/go-activesync/eas/clear_fields_test.go
git commit -m "feat(eas): emit explicit empty elements for cleared event fields"
```

---

### Task 2: `mergeEventDraft` applies explicit clears

**Files:**
- Modify: `vendor/github.com/hstern/go-activesync/eas/calendar.go`
- Test: `vendor/github.com/hstern/go-activesync/eas/clear_fields_test.go`

- [ ] **Step 1: Add the failing test**

Append to `vendor/github.com/hstern/go-activesync/eas/clear_fields_test.go`:

```go
// TestMergeEventDraftClear verifies that clearing zeroes the affected fields
// while preserving untouched ones, and that the clear list is carried through
// so buildEventApp can emit the explicit empty elements.
func TestMergeEventDraftClear(t *testing.T) {
	current := EventItem{
		Subject:     "keep",
		Location:    "old-link",
		BusyStatus:  2,
		Sensitivity: 1,
		Reminder:    15,
		UID:         "abc",
		Attendees:   []EventAttendee{{Email: "a@x"}},
	}

	m := mergeEventDraft(current, EventDraft{
		ClearFields: []string{"location", "attendees", "reminder", "busy_status", "sensitivity"},
	})
	if m.Location != "" {
		t.Errorf("Location not cleared: %q", m.Location)
	}
	if len(m.Attendees) != 0 {
		t.Errorf("Attendees not cleared: %+v", m.Attendees)
	}
	if m.Reminder != 0 {
		t.Errorf("Reminder not cleared: %d", m.Reminder)
	}
	if m.BusyStatus != 0 {
		t.Errorf("BusyStatus not cleared: %d", m.BusyStatus)
	}
	if m.Sensitivity != 0 {
		t.Errorf("Sensitivity not cleared: %d", m.Sensitivity)
	}
	// Untouched fields are preserved.
	if m.Subject != "keep" || m.UID != "abc" {
		t.Errorf("untouched fields lost: subj=%q uid=%q", m.Subject, m.UID)
	}
	// The clear list is carried through for buildEventApp.
	if len(m.ClearFields) != 5 {
		t.Errorf("ClearFields not carried: %+v", m.ClearFields)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./vendor/github.com/hstern/go-activesync/eas/ -run TestMergeEventDraftClear -v` (with `dangerouslyDisableSandbox: true`)
Expected: FAIL — `Location not cleared: "old-link"` (the merge does not apply clears yet).

- [ ] **Step 3: Apply clears in `mergeEventDraft`**

In `vendor/github.com/hstern/go-activesync/eas/calendar.go`, in `mergeEventDraft`, replace the final
```go
	if draft.UID != "" {
		merged.UID = draft.UID
	}
	return merged
}
```
with:
```go
	if draft.UID != "" {
		merged.UID = draft.UID
	}
	// Apply explicit clears last so they win over values seeded from the
	// fetched state. Zeroing here is not sufficient on its own for the
	// conditionally-serialized fields — buildEventApp must also emit an
	// explicit empty element, which it does based on ClearFields.
	for _, f := range draft.ClearFields {
		switch f {
		case "location":
			merged.Location = ""
		case "attendees":
			merged.Attendees = nil
		case "reminder":
			merged.Reminder = 0
		case "sensitivity":
			merged.Sensitivity = 0
		case "busy_status":
			merged.BusyStatus = 0
		}
	}
	merged.ClearFields = draft.ClearFields
	return merged
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./vendor/github.com/hstern/go-activesync/eas/ -run 'TestMergeEventDraftClear|TestBuildEventAppEmitsEmptyClearedFields' -v` (with `dangerouslyDisableSandbox: true`)
Expected: PASS (both).

- [ ] **Step 5: Run the whole eas package to check for regressions**

Run: `go test ./vendor/github.com/hstern/go-activesync/eas/` (with `dangerouslyDisableSandbox: true`)
Expected: `ok  github.com/hstern/go-activesync/eas`.

- [ ] **Step 6: Commit**

```bash
git add vendor/github.com/hstern/go-activesync/eas/calendar.go vendor/github.com/hstern/go-activesync/eas/clear_fields_test.go
git commit -m "feat(eas): apply explicit field clears in the update merge"
```

---

### Task 3: MCP tool `clear` input, validation, and wiring

**Files:**
- Modify: `lib/server/tools_calendar.go`
- Test: `lib/server/tools_calendar_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `lib/server/tools_calendar_test.go`:

```go
// TestCalendarUpdate_clearUnknownField rejects a clear name that is not a
// known clearable field.
func TestCalendarUpdate_clearUnknownField(t *testing.T) {
	mock := &easmock.Client{} // sentinel: any real call is a failure
	m := calendarMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerCalendarTools(s, m.cfg, m)

	ct, st := mcp.NewInMemoryTransports()
	if _, err := s.Connect(t.Context(), st, nil); err != nil {
		t.Fatal(err)
	}
	c := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	cs, err := c.Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "calendar_update_event",
		Arguments: CalendarUpdateEventInput{
			Account: "alpha", FolderID: "cal-id", ID: "cal-id:42",
			Clear: []string{"bogus"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("want IsError for an unknown clear field")
	}
}

// TestCalendarUpdate_clearConflictsWithValue rejects supplying a field both
// as a value and in clear.
func TestCalendarUpdate_clearConflictsWithValue(t *testing.T) {
	mock := &easmock.Client{}
	m := calendarMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerCalendarTools(s, m.cfg, m)

	ct, st := mcp.NewInMemoryTransports()
	if _, err := s.Connect(t.Context(), st, nil); err != nil {
		t.Fatal(err)
	}
	c := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	cs, err := c.Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "calendar_update_event",
		Arguments: CalendarUpdateEventInput{
			Account: "alpha", FolderID: "cal-id", ID: "cal-id:42",
			Location: "https://example.com", Clear: []string{"location"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("want IsError when setting and clearing the same field")
	}
}

// TestCalendarUpdate_clearPropagatesToDraft confirms a valid clear reaches the
// underlying draft.
func TestCalendarUpdate_clearPropagatesToDraft(t *testing.T) {
	var gotDraft eas.EventDraft
	mock := &easmock.Client{
		CalendarClient: easmock.CalendarClient{
			UpdateEventFunc: func(_ context.Context, _, _ string, d eas.EventDraft) error {
				gotDraft = d
				return nil
			},
		},
	}
	m := newMockManager(t, mock, mockManagerOpts{access: config.AccessRW})
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerCalendarTools(s, m.cfg, m)

	callTool(t, s, "calendar_update_event", CalendarUpdateEventInput{
		Account: "alpha", FolderID: "cal-id", ID: "cal-id:42",
		Clear: []string{"location", "attendees"},
	})
	if len(gotDraft.ClearFields) != 2 {
		t.Fatalf("ClearFields = %+v, want 2 entries", gotDraft.ClearFields)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./lib/server/ -run 'TestCalendarUpdate_clear' -v` (with `dangerouslyDisableSandbox: true`)
Expected: compile error — `unknown field Clear in struct literal` (the input field does not exist yet).

- [ ] **Step 3: Add the `Clear` field to the input struct**

In `lib/server/tools_calendar.go`, in `CalendarUpdateEventInput`, add after the `ReminderMin` field:

```go
	Clear       []string           `json:"clear,omitempty" jsonschema:"field names to reset to default: location, attendees, reminder, sensitivity, busy_status"`
```

- [ ] **Step 4: Add the validation helper**

In `lib/server/tools_calendar.go`, immediately before `func registerCalendarUpdate(`, add:

```go
// clearableCalendarFields is the set of fields calendar_update_event may
// reset to their default via the clear input.
var clearableCalendarFields = map[string]bool{
	"location":    true,
	"attendees":   true,
	"reminder":    true,
	"sensitivity": true,
	"busy_status": true,
}

// validateClearFields rejects unknown field names and conflicts between a
// supplied value and a request to clear the same field in one update.
func validateClearFields(in CalendarUpdateEventInput) error {
	for _, name := range in.Clear {
		if !clearableCalendarFields[name] {
			return fmt.Errorf("unknown clear field %q (allowed: location, attendees, reminder, sensitivity, busy_status)", name)
		}
	}
	for _, name := range in.Clear {
		switch name {
		case "location":
			if in.Location != "" {
				return errors.New("cannot set and clear location in the same update")
			}
		case "attendees":
			if len(in.Attendees) > 0 {
				return errors.New("cannot set and clear attendees in the same update")
			}
		case "reminder":
			if in.ReminderMin > 0 {
				return errors.New("cannot set and clear reminder in the same update")
			}
		case "busy_status":
			if in.BusyStatus != 0 {
				return errors.New("cannot set and clear busy_status in the same update")
			}
		}
	}
	return nil
}
```

Both `errors` and `fmt` are already imported in this file.

- [ ] **Step 5: Wire validation and the clear list into the update handler**

In `registerCalendarUpdate`'s handler, right after the `m.CheckClass(...)` guard (and before `c, err := m.Client(...)`), add:

```go
		if err := validateClearFields(in); err != nil {
			return nil, CalendarCreateEventOutput{}, err
		}
```

Then, after the `draft := draftFromInput(...)` assignment, add:

```go
		draft.ClearFields = in.Clear
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./lib/server/ -run 'TestCalendarUpdate_clear' -v` (with `dangerouslyDisableSandbox: true`)
Expected: PASS (all three).

- [ ] **Step 7: Run the whole lib/server package to check for regressions**

Run: `go test ./lib/server/` (with `dangerouslyDisableSandbox: true`)
Expected: `ok  activesync-mcp/lib/server`.

- [ ] **Step 8: Commit**

```bash
git add lib/server/tools_calendar.go lib/server/tools_calendar_test.go
git commit -m "feat(calendar): support clearing fields via calendar_update_event"
```

---

### Task 4: Full verification

**Files:** none (verification only)

- [ ] **Step 1: Build the whole repo**

Run: `go build ./...` (with `dangerouslyDisableSandbox: true`)
Expected: exit 0, no output.

- [ ] **Step 2: Run the affected test packages**

Run: `go test ./vendor/github.com/hstern/go-activesync/eas/ ./lib/server/` (with `dangerouslyDisableSandbox: true`)
Expected: both `ok`.

- [ ] **Step 3: gofmt check on all changed files**

Run: `gofmt -l vendor/github.com/hstern/go-activesync/eas/calendar.go vendor/github.com/hstern/go-activesync/eas/clear_fields_test.go lib/server/tools_calendar.go lib/server/tools_calendar_test.go`
Expected: no output (all formatted).

- [ ] **Step 4: Confirm working tree only touches intended files**

Run: `git status --short`
Expected: only the four files above (plus any pre-existing untracked files unrelated to this work) are modified/staged.
