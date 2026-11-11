// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package server

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"activesync-mcp/lib/config"

	"github.com/hstern/go-activesync/eas"
	"github.com/hstern/go-activesync/eas/easmock"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// calendarFolders is the canned FolderSync output reused by calendar
// tests: an Inbox (filtered out) and a Calendar.
var calendarFolders = &eas.FolderSyncResult{
	SyncKey: "FS",
	Added: []eas.Folder{
		{ServerID: "inbox-id", DisplayName: "Inbox", Type: eas.FolderTypeInbox},
		{ServerID: "cal-id", DisplayName: "Calendar", Type: eas.FolderTypeCalendar},
	},
}

// calendarMockManager is a Manager wired with easmock and per-class
// access map enabling calendar writes.
func calendarMockManager(t *testing.T, c eas.Client) *Manager {
	return newMockManager(t, c, mockManagerOpts{
		access:  config.AccessRO,
		classes: map[string]string{"calendar": config.AccessRW},
	})
}

func TestCalendarListFolders(t *testing.T) {
	mock := &easmock.Client{
		FolderClient: easmock.FolderClient{
			FolderSyncFunc: func(context.Context) (*eas.FolderSyncResult, error) { return calendarFolders, nil },
		},
	}
	m := calendarMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerCalendarTools(s, m.cfg, m)

	out := callTool(t, s, "calendar_list_folders", CalendarListFoldersInput{Account: "alpha"})
	folders := out["folders"].([]any)
	if len(folders) != 1 {
		t.Fatalf("want 1 calendar folder, got %d", len(folders))
	}
	if folders[0].(map[string]any)["display_name"] != "Calendar" {
		t.Errorf("name = %v", folders[0])
	}
}

func TestCalendarListEvents(t *testing.T) {
	mock := &easmock.Client{
		CalendarClient: easmock.CalendarClient{
			SyncCalendarFunc: func(_ context.Context, fid string, _ eas.CalendarSyncOptions) (*eas.CalendarSyncResult, error) {
				if fid != "cal-id" {
					t.Errorf("folder = %q", fid)
				}
				start := time.Date(2026, 5, 9, 14, 0, 0, 0, time.UTC)
				return &eas.CalendarSyncResult{
					SyncKey: "C2",
					Added: []eas.EventItem{
						{
							ServerID:  "cal-id:42",
							Subject:   "Quarterly review",
							StartTime: start,
							EndTime:   start.Add(time.Hour),
						},
					},
				}, nil
			},
		},
	}
	m := calendarMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerCalendarTools(s, m.cfg, m)

	out := callTool(t, s, "calendar_list_events", CalendarListEventsInput{
		Account: "alpha", FolderID: "cal-id",
	})
	events := out["events"].([]any)
	if len(events) != 1 {
		t.Fatalf("len(events) = %d", len(events))
	}
	if events[0].(map[string]any)["subject"] != "Quarterly review" {
		t.Errorf("subject = %v", events[0])
	}
}

func TestCalendarDateWindow(t *testing.T) {
	for _, tc := range []struct {
		in        string
		want      eas.FilterType
		applied   string
		broadened bool
	}{
		{"", eas.FilterTwoWeek, "2w", false},
		{"none", eas.FilterNone, "none", false},
		{"1d", eas.FilterTwoWeek, "2w", true},
		{"3d", eas.FilterTwoWeek, "2w", true},
		{"1w", eas.FilterTwoWeek, "2w", true},
		{"2w", eas.FilterTwoWeek, "2w", false},
		{"1m", eas.FilterOneMonth, "1m", false},
		{"3m", eas.FilterThreeMonth, "3m", false},
		{"6m", eas.FilterSixMonth, "6m", false},
		{"bogus", eas.FilterTwoWeek, "2w", false},
	} {
		f, applied, broadened := calendarDateWindow(tc.in)
		if f != tc.want || applied != tc.applied || broadened != tc.broadened {
			t.Errorf("calendarDateWindow(%q) = (%d, %q, %v), want (%d, %q, %v)",
				tc.in, f, applied, broadened, tc.want, tc.applied, tc.broadened)
		}
	}
}

func TestCalendarListEvents_broadensShortWindow(t *testing.T) {
	var gotFilter eas.FilterType
	mock := &easmock.Client{
		CalendarClient: easmock.CalendarClient{
			SyncCalendarFunc: func(_ context.Context, _ string, opts eas.CalendarSyncOptions) (*eas.CalendarSyncResult, error) {
				gotFilter = opts.DateFilter
				return &eas.CalendarSyncResult{SyncKey: "C2"}, nil
			},
		},
	}
	m := calendarMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerCalendarTools(s, m.cfg, m)

	out := callTool(t, s, "calendar_list_events", CalendarListEventsInput{
		Account: "alpha", FolderID: "cal-id", DateWindow: "1d",
	})
	if gotFilter != eas.FilterTwoWeek {
		t.Errorf("server saw filter %d, want TwoWeek (1d must broaden)", gotFilter)
	}
	if out["date_window_applied"] != "2w" {
		t.Errorf("date_window_applied = %v, want \"2w\"", out["date_window_applied"])
	}
}

func TestCalendarListEvents_supportedWindowUnflagged(t *testing.T) {
	mock := &easmock.Client{
		CalendarClient: easmock.CalendarClient{
			SyncCalendarFunc: func(_ context.Context, _ string, opts eas.CalendarSyncOptions) (*eas.CalendarSyncResult, error) {
				if opts.DateFilter != eas.FilterOneMonth {
					t.Errorf("server saw filter %d, want OneMonth", opts.DateFilter)
				}
				return &eas.CalendarSyncResult{SyncKey: "C2"}, nil
			},
		},
	}
	m := calendarMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerCalendarTools(s, m.cfg, m)

	out := callTool(t, s, "calendar_list_events", CalendarListEventsInput{
		Account: "alpha", FolderID: "cal-id", DateWindow: "1m",
	})
	if _, present := out["date_window_applied"]; present {
		t.Errorf("date_window_applied present (%v) for a supported window", out["date_window_applied"])
	}
}

func TestCalendarRespondInvite_validation(t *testing.T) {
	mock := &easmock.Client{
		CalendarClient: easmock.CalendarClient{
			RespondInviteFunc: func(_ context.Context, fid, sid string, _ eas.MeetingResponseChoice) (*eas.MeetingResponseResult, error) {
				if fid != "inbox-id" || sid != "invite" {
					t.Errorf("got fid=%q sid=%q", fid, sid)
				}
				return &eas.MeetingResponseResult{Status: 1, CalendarID: "cal-id:42"}, nil
			},
		},
	}
	m := calendarMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerCalendarTools(s, m.cfg, m)

	out := callTool(t, s, "calendar_respond_invite", CalendarRespondInviteInput{
		Account: "alpha", FolderID: "inbox-id", ID: "invite", Response: "accept",
	})
	if out["calendar_id"] != "cal-id:42" {
		t.Errorf("calendar_id = %v", out["calendar_id"])
	}
}

func TestParseInviteResponse(t *testing.T) {
	for _, c := range []struct {
		in string
		ok bool
	}{
		{"accept", true},
		{"Tentative", true},
		{"DECLINE", true},
		{"1", true},
		{"yes", false},
		{"", false},
	} {
		_, err := parseInviteResponse(c.in)
		if (err == nil) != c.ok {
			t.Errorf("parseInviteResponse(%q): err=%v, want ok=%v", c.in, err, c.ok)
		}
	}
}

func TestCalendarCreate_requiresStartEnd(t *testing.T) {
	mock := &easmock.Client{} // no Func — sentinel fires if anything is called
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
		Name: "calendar_create_event",
		Arguments: CalendarCreateEventInput{
			Account: "alpha", FolderID: "cal-id", Subject: "x",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("want IsError when start/end missing")
	}
}

func TestDraftFromInput_mapsAllFields(t *testing.T) {
	start := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	in := CalendarCreateEventInput{
		Subject:    "Standup",
		Location:   "Zoom",
		Body:       "agenda",
		StartTime:  start,
		EndTime:    end,
		AllDay:     false,
		BusyStatus: 2,
		Attendees: []CalendarAttendee{
			{Name: "Alice", Email: "alice@x"},
			{Email: "bob@x"},
		},
		ReminderMin: 10,
	}
	d := draftFromInput(in)
	if d.Subject != "Standup" || d.Location != "Zoom" || d.BusyStatus != 2 {
		t.Errorf("scalar fields wrong: %+v", d)
	}
	if !d.StartTime.Equal(start) || !d.EndTime.Equal(end) {
		t.Errorf("time fields wrong: %+v", d)
	}
	if d.Reminder != 10 {
		t.Errorf("reminder = %d, want 10", d.Reminder)
	}
	if len(d.Attendees) != 2 || d.Attendees[0].Name != "Alice" || d.Attendees[1].Email != "bob@x" {
		t.Errorf("attendees = %+v", d.Attendees)
	}
}

func TestCalendarCreate_success(t *testing.T) {
	mock := &easmock.Client{
		CalendarClient: easmock.CalendarClient{
			CreateEventFunc: func(_ context.Context, fid string, _ eas.EventDraft) (string, error) {
				if fid != "cal-id" {
					t.Errorf("folder = %q", fid)
				}
				return "cal-id:99", nil
			},
		},
	}
	m := newMockManager(t, mock, mockManagerOpts{access: config.AccessRW})
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerCalendarTools(s, m.cfg, m)

	start := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	out := callTool(t, s, "calendar_create_event", CalendarCreateEventInput{
		Account: "alpha", FolderID: "cal-id",
		Subject: "Plan", StartTime: start, EndTime: start.Add(time.Hour),
	})
	if out["id"] != "cal-id:99" {
		t.Errorf("id = %v, want cal-id:99", out["id"])
	}
}

func TestCalendarUpdate_success(t *testing.T) {
	mock := &easmock.Client{
		CalendarClient: easmock.CalendarClient{
			UpdateEventFunc: func(_ context.Context, fid, sid string, _ eas.EventDraft) error {
				if fid != "cal-id" || sid != "cal-id:42" {
					t.Errorf("got fid=%q sid=%q", fid, sid)
				}
				return nil
			},
		},
	}
	m := newMockManager(t, mock, mockManagerOpts{access: config.AccessRW})
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerCalendarTools(s, m.cfg, m)

	start := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	callTool(t, s, "calendar_update_event", CalendarUpdateEventInput{
		Account: "alpha", FolderID: "cal-id", ID: "cal-id:42",
		Subject: "Plan (updated)", StartTime: start, EndTime: start.Add(time.Hour),
	})
}

func TestCalendarDelete_success(t *testing.T) {
	mock := &easmock.Client{
		CalendarClient: easmock.CalendarClient{
			DeleteEventFunc: func(_ context.Context, fid, sid string) error {
				if fid != "cal-id" || sid != "cal-id:42" {
					t.Errorf("got fid=%q sid=%q", fid, sid)
				}
				return nil
			},
		},
	}
	m := newMockManager(t, mock, mockManagerOpts{access: config.AccessRW})
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerCalendarTools(s, m.cfg, m)

	callTool(t, s, "calendar_delete_event", CalendarDeleteEventInput{
		Account: "alpha", FolderID: "cal-id", ID: "cal-id:42",
	})
}

func TestEventRowFrom_organizerAndAttendees(t *testing.T) {
	row := eventRowFrom(eas.EventItem{
		ServerID:       "id",
		Subject:        "S",
		OrganizerName:  "Alice",
		OrganizerEmail: "alice@x",
		Attendees: []eas.EventAttendee{
			{Name: "Bob", Email: "bob@x"},
			{Email: "carol@x"},
		},
	})
	if row.Organizer != "Alice <alice@x>" {
		t.Errorf("Organizer = %q", row.Organizer)
	}
	if len(row.Attendees) != 2 {
		t.Fatalf("len(Attendees) = %d", len(row.Attendees))
	}
	if !strings.Contains(row.Attendees[0], "Bob") || !strings.Contains(row.Attendees[1], "carol@x") {
		t.Errorf("attendees = %v", row.Attendees)
	}
}

// Error-wrap tests: each handler that calls into the EAS layer wraps
// the error as `fmt.Errorf("Cmd: %w", err)`. The wrap is invisible
// until something fails in production — these tests catch typos in the
// wrap message before they ship.

func TestCalendarListFolders_wrapsError(t *testing.T) {
	mock := &easmock.Client{FolderClient: easmock.FolderClient{
		FolderSyncFunc: func(context.Context) (*eas.FolderSyncResult, error) {
			return nil, errors.New("boom")
		},
	}}
	m := calendarMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerCalendarTools(s, m.cfg, m)
	res := callToolErr(t, s, "calendar_list_folders", CalendarListFoldersInput{Account: "alpha"})
	txt := errText(t, res)
	if !strings.Contains(txt, "FolderSync") || !strings.Contains(txt, "boom") {
		t.Errorf("err text = %q", txt)
	}
}

func TestCalendarListEvents_wrapsError(t *testing.T) {
	mock := &easmock.Client{CalendarClient: easmock.CalendarClient{
		SyncCalendarFunc: func(context.Context, string, eas.CalendarSyncOptions) (*eas.CalendarSyncResult, error) {
			return nil, errors.New("boom")
		},
	}}
	m := calendarMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerCalendarTools(s, m.cfg, m)
	res := callToolErr(t, s, "calendar_list_events", CalendarListEventsInput{
		Account: "alpha", FolderID: "cal-id",
	})
	txt := errText(t, res)
	if !strings.Contains(txt, "SyncCalendar") {
		t.Errorf("err text = %q", txt)
	}
}

func TestCalendarCreate_wrapsError(t *testing.T) {
	mock := &easmock.Client{CalendarClient: easmock.CalendarClient{
		CreateEventFunc: func(context.Context, string, eas.EventDraft) (string, error) {
			return "", errors.New("boom")
		},
	}}
	m := newMockManager(t, mock, mockManagerOpts{access: config.AccessRW})
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerCalendarTools(s, m.cfg, m)
	start := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	res := callToolErr(t, s, "calendar_create_event", CalendarCreateEventInput{
		Account: "alpha", FolderID: "cal-id", Subject: "x",
		StartTime: start, EndTime: start.Add(time.Hour),
	})
	txt := errText(t, res)
	if !strings.Contains(txt, "CreateEvent") {
		t.Errorf("err text = %q", txt)
	}
}

func TestCalendarUpdate_wrapsError(t *testing.T) {
	mock := &easmock.Client{CalendarClient: easmock.CalendarClient{
		UpdateEventFunc: func(context.Context, string, string, eas.EventDraft) error {
			return errors.New("boom")
		},
	}}
	m := newMockManager(t, mock, mockManagerOpts{access: config.AccessRW})
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerCalendarTools(s, m.cfg, m)
	start := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	res := callToolErr(t, s, "calendar_update_event", CalendarUpdateEventInput{
		Account: "alpha", FolderID: "cal-id", ID: "id",
		StartTime: start, EndTime: start.Add(time.Hour),
	})
	if !strings.Contains(errText(t, res), "UpdateEvent") {
		t.Errorf("err text = %q", errText(t, res))
	}
}

func TestCalendarDelete_wrapsError(t *testing.T) {
	mock := &easmock.Client{CalendarClient: easmock.CalendarClient{
		DeleteEventFunc: func(context.Context, string, string) error {
			return errors.New("boom")
		},
	}}
	m := newMockManager(t, mock, mockManagerOpts{access: config.AccessRW})
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerCalendarTools(s, m.cfg, m)
	res := callToolErr(t, s, "calendar_delete_event", CalendarDeleteEventInput{
		Account: "alpha", FolderID: "cal-id", ID: "id",
	})
	if !strings.Contains(errText(t, res), "DeleteEvent") {
		t.Errorf("err text = %q", errText(t, res))
	}
}

func TestCalendarRespondInvite_wrapsError(t *testing.T) {
	mock := &easmock.Client{CalendarClient: easmock.CalendarClient{
		RespondInviteFunc: func(context.Context, string, string, eas.MeetingResponseChoice) (*eas.MeetingResponseResult, error) {
			return nil, errors.New("boom")
		},
	}}
	m := calendarMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerCalendarTools(s, m.cfg, m)
	res := callToolErr(t, s, "calendar_respond_invite", CalendarRespondInviteInput{
		Account: "alpha", FolderID: "i", ID: "x", Response: "accept",
	})
	if !strings.Contains(errText(t, res), "RespondInvite") {
		t.Errorf("err text = %q", errText(t, res))
	}
}

// TestCalendarUpdate_clearUnknownField rejects a clear name that is not a
// known clearable field.
func TestCalendarUpdate_clearUnknownField(t *testing.T) {
	mock := &easmock.Client{} // sentinel: any real call is a failure
	m := calendarMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerCalendarTools(s, m.cfg, m)

	res := callToolErr(t, s, "calendar_update_event", CalendarUpdateEventInput{
		Account: "alpha", FolderID: "cal-id", ID: "cal-id:42",
		Clear: []string{"bogus"},
	})
	if !strings.Contains(errText(t, res), "unknown clear field") {
		t.Errorf("err text = %q", errText(t, res))
	}
}

// TestCalendarUpdate_clearConflictsWithValue rejects supplying a field both
// as a value and in clear.
func TestCalendarUpdate_clearConflictsWithValue(t *testing.T) {
	mock := &easmock.Client{}
	m := calendarMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerCalendarTools(s, m.cfg, m)

	res := callToolErr(t, s, "calendar_update_event", CalendarUpdateEventInput{
		Account: "alpha", FolderID: "cal-id", ID: "cal-id:42",
		Location: "https://example.com", Clear: []string{"location"},
	})
	if !strings.Contains(errText(t, res), "cannot set and clear location") {
		t.Errorf("err text = %q", errText(t, res))
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
	if !slices.Equal(gotDraft.ClearFields, []string{"location", "attendees"}) {
		t.Fatalf("ClearFields = %+v, want [location attendees]", gotDraft.ClearFields)
	}
}

// TestCalendarUpdate_doesNotOverrideTimeZone pins the read-modify-write
// contract for the time zone: the update path must leave TimeZoneRaw empty so
// UpdateEvent round-trips the raw blob the server reported, instead of
// overwriting the item's zone with the host's local zone (which draftFromInput
// attaches because Exchange requires one on calendar Add).
func TestCalendarUpdate_doesNotOverrideTimeZone(t *testing.T) {
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
		Location: "https://example.com",
	})
	if gotDraft.TimeZoneRaw != "" {
		t.Errorf("update draft TimeZoneRaw = %q, want empty (server blob must win)", gotDraft.TimeZoneRaw)
	}
	if gotDraft.TimeZone != nil {
		t.Errorf("update draft TimeZone = %+v, want nil", gotDraft.TimeZone)
	}
}

// TestCalendarCreate_keepsLocalTimeZone guards the companion half of the same
// decision: creates keep attaching the host's local zone, which Exchange
// requires on calendar Add (the all-zero UTC blob is rejected with item
// status 6).
func TestCalendarCreate_keepsLocalTimeZone(t *testing.T) {
	var gotDraft eas.EventDraft
	mock := &easmock.Client{
		CalendarClient: easmock.CalendarClient{
			CreateEventFunc: func(_ context.Context, _ string, d eas.EventDraft) (string, error) {
				gotDraft = d
				return "cal-id:99", nil
			},
		},
	}
	m := newMockManager(t, mock, mockManagerOpts{access: config.AccessRW})
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerCalendarTools(s, m.cfg, m)

	start := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	callTool(t, s, "calendar_create_event", CalendarCreateEventInput{
		Account: "alpha", FolderID: "cal-id",
		Subject: "Plan", StartTime: start, EndTime: start.Add(time.Hour),
	})
	if gotDraft.TimeZoneRaw == "" {
		t.Error("create draft TimeZoneRaw is empty, want the host's local zone")
	}
}

// TestCalendarListEvents_expandsRecurringSeries confirms a recurring item is
// returned as one row per in-window occurrence with id suffixes and a
// series_id, while single events pass through untouched.
func TestCalendarListEvents_expandsRecurringSeries(t *testing.T) {
	mock := &easmock.Client{
		CalendarClient: easmock.CalendarClient{
			SyncCalendarFunc: func(_ context.Context, _ string, _ eas.CalendarSyncOptions) (*eas.CalendarSyncResult, error) {
				anchor := time.Date(2025, 10, 9, 7, 30, 0, 0, time.UTC) // Thursday
				return &eas.CalendarSyncResult{
					SyncKey: "C2",
					Added: []eas.EventItem{
						{
							ServerID:  "cal-id:42",
							Subject:   "Еженедельный статус (производство)",
							StartTime: anchor,
							EndTime:   anchor.Add(90 * time.Minute),
							Recurrence: &eas.Recurrence{
								Type:      eas.RecurrenceWeekly,
								Interval:  1,
								DayOfWeek: eas.DowThursday,
							},
						},
						{
							ServerID:  "cal-id:7",
							Subject:   "One-off",
							StartTime: time.Date(2026, 11, 12, 16, 0, 0, 0, time.UTC),
							EndTime:   time.Date(2026, 11, 12, 17, 0, 0, 0, time.UTC),
						},
					},
				}, nil
			},
		},
	}
	m := calendarMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerCalendarTools(s, m.cfg, m)

	out := callTool(t, s, "calendar_list_events", CalendarListEventsInput{
		Account: "alpha", FolderID: "cal-id",
	})
	events := out["events"].([]any)
	type row struct {
		ID        string    `json:"id"`
		Subject   string    `json:"subject"`
		SeriesID  string    `json:"series_id"`
		StartTime time.Time `json:"start_time"`
	}
	var rows []row
	for _, e := range events {
		var r row
		b, _ := json.Marshal(e)
		_ = json.Unmarshal(b, &r)
		rows = append(rows, r)
	}
	// Default window is [today, today+14d) in UTC: a weekly series always
	// yields exactly two in-window Thursdays regardless of the current
	// date. Pin the full occurrence ids, not just the prefix.
	today := time.Now().UTC().Truncate(24 * time.Hour)
	first := today.Add(time.Duration((int(time.Thursday)-int(today.Weekday())+7)%7) * 24 * time.Hour)
	wantIDs := map[string]bool{
		"cal-id:42@" + first.Format("2006-01-02"):                  false,
		"cal-id:42@" + first.AddDate(0, 0, 7).Format("2006-01-02"): false,
	}
	var seriesRows, oneOff int
	for _, r := range rows {
		switch {
		case r.SeriesID == "cal-id:42":
			seriesRows++
			if _, ok := wantIDs[r.ID]; !ok {
				t.Errorf("series row id %q is not one of the expected occurrence ids", r.ID)
			}
			wantIDs[r.ID] = true
			if st := r.StartTime; st.Hour() != 7 || st.Minute() != 30 || st.Location() != time.UTC {
				t.Errorf("series row %q start_time = %v, want the anchor's 07:30 UTC time-of-day", r.ID, st)
			}
		case r.ID == "cal-id:7":
			oneOff++
			if r.SeriesID != "" {
				t.Errorf("one-off row carries series_id %q", r.SeriesID)
			}
		default:
			t.Errorf("unexpected row %+v", r)
		}
	}
	if seriesRows != 2 {
		t.Errorf("expanded series rows = %d, want 2", seriesRows)
	}
	for id, seen := range wantIDs {
		if !seen {
			t.Errorf("expected occurrence id %q missing from the response", id)
		}
	}
	if oneOff != 1 {
		t.Errorf("one-off rows = %d, want 1", oneOff)
	}
	if len(rows) != seriesRows+oneOff {
		t.Errorf("total rows = %d, want %d (series %d + one-off %d)", len(rows), seriesRows+oneOff, seriesRows, oneOff)
	}
}

// TestCalendarListEvents_recurringAnchorFallback pins the safety net: when a
// series has no occurrence in the window (its Until is in the past), the
// anchor row is still shown rather than vanishing.
func TestCalendarListEvents_recurringAnchorFallback(t *testing.T) {
	mock := &easmock.Client{
		CalendarClient: easmock.CalendarClient{
			SyncCalendarFunc: func(_ context.Context, _ string, _ eas.CalendarSyncOptions) (*eas.CalendarSyncResult, error) {
				anchor := time.Date(2025, 1, 2, 9, 0, 0, 0, time.UTC)
				until := time.Date(2025, 6, 30, 10, 0, 0, 0, time.UTC)
				return &eas.CalendarSyncResult{
					SyncKey: "C2",
					Added: []eas.EventItem{
						{
							ServerID:  "cal-id:9",
							Subject:   "Закончившаяся серия",
							StartTime: anchor,
							EndTime:   anchor.Add(time.Hour),
							Recurrence: &eas.Recurrence{
								Type:      eas.RecurrenceWeekly,
								Interval:  1,
								DayOfWeek: eas.DowMonday,
								Until:     until,
							},
						},
					},
				}, nil
			},
		},
	}
	m := calendarMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerCalendarTools(s, m.cfg, m)

	out := callTool(t, s, "calendar_list_events", CalendarListEventsInput{
		Account: "alpha", FolderID: "cal-id",
	})
	events := out["events"].([]any)
	if len(events) != 1 {
		t.Fatalf("want the anchor row, got %d rows", len(events))
	}
	row := events[0].(map[string]any)
	if row["id"] != "cal-id:9" {
		t.Errorf("anchor row id = %v, want cal-id:9", row["id"])
	}
}
