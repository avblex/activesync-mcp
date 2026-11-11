package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"activesync-mcp/lib/config"
	"github.com/hstern/go-activesync/eas"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerCalendarTools wires calendar list/CRUD plus invite-respond.
// Read tools are registered if any account exists; write tools only if
// at least one account permits writes for the calendar class.
func registerCalendarTools(s *mcp.Server, cfg *config.Config, m *Manager) {
	all := cfg.AccountNames()
	if len(all) == 0 {
		return
	}
	registerCalendarListFolders(s, m, all)
	registerCalendarListEvents(s, m, all)
	writers := cfg.WritableAccounts(config.ClassCalendar)
	if len(writers) > 0 {
		registerCalendarCreate(s, m, writers)
		registerCalendarUpdate(s, m, writers)
		registerCalendarDelete(s, m, writers)
		registerCalendarRespondInvite(s, m, writers)
	}
}

// --- calendar_list_folders -------------------------------------------------

// CalendarListFoldersInput is the schema for calendar_list_folders.
type CalendarListFoldersInput struct {
	Account string `json:"account"`
}

// CalendarFolderRow describes a calendar folder.
type CalendarFolderRow struct {
	ID          string `json:"id"`
	ParentID    string `json:"parent_id,omitempty"`
	DisplayName string `json:"display_name"`
	Type        string `json:"type"`
	TypeCode    int    `json:"type_code"`
}

// CalendarListFoldersOutput wraps the folder list.
type CalendarListFoldersOutput struct {
	Folders []CalendarFolderRow `json:"folders"`
}

func registerCalendarListFolders(s *mcp.Server, m *Manager, accounts []string) {
	tool := &mcp.Tool{
		Name:        "calendar_list_folders",
		Description: "List the calendar folders for an account.",
	}
	scopeEnum(tool, "account", accounts)
	mcp.AddTool(s, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in CalendarListFoldersInput) (*mcp.CallToolResult, CalendarListFoldersOutput, error) {
		folders, err := m.SyncFolderList(ctx, in.Account)
		if err != nil {
			return nil, CalendarListFoldersOutput{}, err
		}
		out := CalendarListFoldersOutput{}
		for _, f := range folders {
			if !isCalendarFolder(f.Type) {
				continue
			}
			out.Folders = append(out.Folders, CalendarFolderRow{
				ID:          f.ServerID,
				ParentID:    f.ParentID,
				DisplayName: f.DisplayName,
				Type:        f.Type.String(),
				TypeCode:    int(f.Type),
			})
		}
		return jsonResult(out)
	})
}

func isCalendarFolder(t eas.FolderType) bool {
	return t == eas.FolderTypeCalendar || t == eas.FolderTypeUserCalendar
}

// --- calendar_list_events --------------------------------------------------

// CalendarListEventsInput is the schema for calendar_list_events.
type CalendarListEventsInput struct {
	Account    string `json:"account"`
	FolderID   string `json:"folder_id"`
	WindowSize int    `json:"window_size,omitempty" jsonschema:"max events per response (default 100)"`
	DateWindow string `json:"date_window,omitempty" jsonschema:"none, 1d, 3d, 1w, 2w, 1m, 3m, 6m (default 2w)"`
	Cursor     string `json:"cursor,omitempty" jsonschema:"pagination cursor; omit for the most recent batch, pass back the sync_cursor from a prior response to fetch the next batch"`
}

// EventRow is one event in the list response.
type EventRow struct {
	ID         string    `json:"id"`
	Subject    string    `json:"subject"`
	Location   string    `json:"location,omitempty"`
	StartTime  time.Time `json:"start_time,omitzero"`
	EndTime    time.Time `json:"end_time,omitzero"`
	AllDay     bool      `json:"all_day,omitempty"`
	BusyStatus int       `json:"busy_status,omitempty"`
	Organizer  string    `json:"organizer,omitempty"`
	Attendees  []string  `json:"attendees,omitempty"`
	// SeriesID is set for rows expanded from a recurring series and holds
	// the bare server id of the series anchor. Update/delete operations
	// take this id, not the occurrence id.
	SeriesID string `json:"series_id,omitempty"`
}

// CalendarListEventsOutput wraps the rows.
type CalendarListEventsOutput struct {
	Events        []EventRow `json:"events"`
	MoreAvailable bool       `json:"more_available"`
	SyncCursor    string     `json:"sync_cursor"`
	// DateWindowApplied reports the window the server actually filtered
	// with. It differs from the requested date_window when the request
	// asked for a span the calendar collection does not support (see
	// calendarDateWindow) — e.g. "1d" comes back as "2w".
	DateWindowApplied string `json:"date_window_applied,omitempty"`
}

// calendarDateWindow maps a requested window onto the closest supported
// calendar window. MS-ASAIRS §2.2.2.5 defines the calendar FilterType set
// as None/TwoWeek/OneMonth/ThreeMonth/SixMonth — there are no one-day,
// three-day, or one-week calendar filters, and Exchange rejects them with
// Sync status 4 (ProtocolError). Short requests silently broaden to two
// weeks; the caller sees the substitution in date_window_applied.
func calendarDateWindow(s string) (eas.FilterType, string, bool) {
	switch s {
	case "", "2w":
		return eas.FilterTwoWeek, "2w", false
	case "none":
		return eas.FilterNone, "none", false
	case "1d", "3d", "1w":
		return eas.FilterTwoWeek, "2w", true
	case "1m":
		return eas.FilterOneMonth, "1m", false
	case "3m":
		return eas.FilterThreeMonth, "3m", false
	case "6m":
		return eas.FilterSixMonth, "6m", false
	}
	return eas.FilterTwoWeek, "2w", false
}

func registerCalendarListEvents(s *mcp.Server, m *Manager, accounts []string) {
	tool := &mcp.Tool{
		Name: "calendar_list_events",
		Description: "List calendar events from a folder. " +
			"Use date_window to limit recency. Recurring series are expanded " +
			"into one row per occurrence within the window; such rows carry a " +
			"series_id (the bare id to use for update/delete) and an id " +
			"suffixed with the occurrence date.",
	}
	scopeEnum(tool, "account", accounts)
	mcp.AddTool(s, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in CalendarListEventsInput) (*mcp.CallToolResult, CalendarListEventsOutput, error) {
		c, err := m.Client(ctx, in.Account)
		if err != nil {
			return nil, CalendarListEventsOutput{}, err
		}
		if err := m.PrepareListCursor(ctx, in.Account, in.FolderID, in.Cursor); err != nil {
			return nil, CalendarListEventsOutput{}, fmt.Errorf("PrepareListCursor: %w", err)
		}
		filter, applied, broadened := calendarDateWindow(in.DateWindow)
		res, err := c.SyncCalendar(ctx, in.FolderID, eas.CalendarSyncOptions{
			WindowSize: in.WindowSize,
			DateFilter: filter,
		})
		if err != nil {
			return nil, CalendarListEventsOutput{}, fmt.Errorf("SyncCalendar: %w", err)
		}
		out := CalendarListEventsOutput{
			MoreAvailable: res.MoreAvailable,
			SyncCursor:    res.SyncKey,
		}
		if broadened {
			out.DateWindowApplied = applied
		}
		winStart, winEnd := expansionWindow(filter, time.Now())
		for _, ev := range res.Added {
			out.Events = append(out.Events, eventRowsFrom(ev, winStart, winEnd)...)
		}
		for _, ev := range res.Changed {
			out.Events = append(out.Events, eventRowsFrom(ev, winStart, winEnd)...)
		}
		return jsonResult(out)
	})
}

// expansionWindow derives the client-side expansion window from the
// requested date window. The right edge mirrors what the server was asked
// about; "none" (FilterNone) has no server-side bound, so infinite series
// get a fixed one-year horizon.
func expansionWindow(filter eas.FilterType, now time.Time) (time.Time, time.Time) {
	start := now.Truncate(24 * time.Hour)
	var end time.Time
	switch filter {
	case eas.FilterOneDay:
		end = start.Add(24 * time.Hour)
	case eas.FilterThreeDay:
		end = start.Add(3 * 24 * time.Hour)
	case eas.FilterOneWeek:
		end = start.Add(7 * 24 * time.Hour)
	case eas.FilterTwoWeek:
		end = start.Add(14 * 24 * time.Hour)
	case eas.FilterOneMonth:
		end = start.AddDate(0, 1, 0)
	case eas.FilterThreeMonth:
		end = start.AddDate(0, 3, 0)
	case eas.FilterSixMonth:
		end = start.AddDate(0, 6, 0)
	default: // FilterNone
		end = start.AddDate(1, 0, 0)
	}
	return start, end
}

// eventRowsFrom expands a recurring item into one row per in-window
// occurrence (id suffixed with the occurrence date, series_id carrying the
// anchor id) and falls back to the plain anchor row when nothing falls in
// the window, so a series never vanishes from the list silently.
func eventRowsFrom(ev eas.EventItem, winStart, winEnd time.Time) []EventRow {
	if ev.Recurrence == nil {
		return []EventRow{eventRowFrom(ev)}
	}
	var rows []EventRow
	for _, occ := range eas.ExpandRecurrences(ev, winStart, winEnd) {
		row := eventRowFrom(occ)
		row.SeriesID = ev.ServerID
		row.ID = ev.ServerID + "@" + occ.StartTime.UTC().Format("2006-01-02")
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		rows = append(rows, eventRowFrom(ev))
	}
	return rows
}

func eventRowFrom(ev eas.EventItem) EventRow {
	row := EventRow{
		ID:         ev.ServerID,
		Subject:    ev.Subject,
		Location:   ev.Location,
		StartTime:  ev.StartTime,
		EndTime:    ev.EndTime,
		AllDay:     ev.AllDayEvent,
		BusyStatus: ev.BusyStatus,
	}
	if ev.OrganizerEmail != "" || ev.OrganizerName != "" {
		row.Organizer = strings.TrimSpace(ev.OrganizerName + " <" + ev.OrganizerEmail + ">")
	}
	for _, a := range ev.Attendees {
		row.Attendees = append(row.Attendees, strings.TrimSpace(a.Name+" <"+a.Email+">"))
	}
	return row
}

// --- calendar_create_event -------------------------------------------------

// CalendarAttendee is one attendee on a created/updated event.
type CalendarAttendee struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email"`
}

// CalendarCreateEventInput is the schema for calendar_create_event.
type CalendarCreateEventInput struct {
	Account     string             `json:"account"`
	FolderID    string             `json:"folder_id"`
	Subject     string             `json:"subject"`
	Location    string             `json:"location,omitempty"`
	Body        string             `json:"body,omitempty"`
	StartTime   time.Time          `json:"start_time" jsonschema:"RFC3339; required"`
	EndTime     time.Time          `json:"end_time" jsonschema:"RFC3339; required"`
	AllDay      bool               `json:"all_day,omitempty"`
	BusyStatus  int                `json:"busy_status,omitempty" jsonschema:"busy code (0 free, 1 tentative, 2 busy, 3 OOF)"`
	Attendees   []CalendarAttendee `json:"attendees,omitempty"`
	ReminderMin int                `json:"reminder_minutes,omitempty"`
}

// CalendarCreateEventOutput reports the new event id.
type CalendarCreateEventOutput struct {
	ID string `json:"id"`
}

func registerCalendarCreate(s *mcp.Server, m *Manager, accounts []string) {
	tool := &mcp.Tool{
		Name:        "calendar_create_event",
		Description: "Create a new calendar event.",
	}
	scopeEnum(tool, "account", accounts)
	mcp.AddTool(s, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in CalendarCreateEventInput) (*mcp.CallToolResult, CalendarCreateEventOutput, error) {
		if err := m.CheckClass(in.Account, config.ClassCalendar, true); err != nil {
			return nil, CalendarCreateEventOutput{}, err
		}
		if in.StartTime.IsZero() || in.EndTime.IsZero() {
			return nil, CalendarCreateEventOutput{}, errors.New("start_time and end_time are required")
		}
		c, err := m.Client(ctx, in.Account)
		if err != nil {
			return nil, CalendarCreateEventOutput{}, err
		}
		id, err := c.CreateEvent(ctx, in.FolderID, draftFromInput(in))
		if err != nil {
			return nil, CalendarCreateEventOutput{}, fmt.Errorf("CreateEvent: %w", err)
		}
		return jsonResult(CalendarCreateEventOutput{ID: id})
	})
}

// CalendarUpdateEventInput is the schema for calendar_update_event.
type CalendarUpdateEventInput struct {
	Account     string             `json:"account"`
	FolderID    string             `json:"folder_id"`
	ID          string             `json:"id"`
	Subject     string             `json:"subject,omitempty"`
	Location    string             `json:"location,omitempty"`
	Body        string             `json:"body,omitempty"`
	StartTime   time.Time          `json:"start_time,omitzero"`
	EndTime     time.Time          `json:"end_time,omitzero"`
	AllDay      bool               `json:"all_day,omitempty"`
	BusyStatus  int                `json:"busy_status,omitempty"`
	Attendees   []CalendarAttendee `json:"attendees,omitempty"`
	ReminderMin int                `json:"reminder_minutes,omitempty"`
	Clear       []string           `json:"clear,omitempty" jsonschema:"field names to reset to default: location, attendees, reminder, sensitivity, busy_status"`
}

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

func registerCalendarUpdate(s *mcp.Server, m *Manager, accounts []string) {
	tool := &mcp.Tool{
		Name: "calendar_update_event",
		Description: "Modify an existing calendar event. Pass only the fields you want to change " +
			"(except start/end time, which must both be supplied to update timing).",
	}
	scopeEnum(tool, "account", accounts)
	mcp.AddTool(s, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in CalendarUpdateEventInput) (*mcp.CallToolResult, CalendarCreateEventOutput, error) {
		if err := m.CheckClass(in.Account, config.ClassCalendar, true); err != nil {
			return nil, CalendarCreateEventOutput{}, err
		}
		if err := validateClearFields(in); err != nil {
			return nil, CalendarCreateEventOutput{}, err
		}
		c, err := m.Client(ctx, in.Account)
		if err != nil {
			return nil, CalendarCreateEventOutput{}, err
		}
		draft := draftFromInput(CalendarCreateEventInput{
			Subject: in.Subject, Location: in.Location, Body: in.Body,
			StartTime: in.StartTime, EndTime: in.EndTime,
			AllDay: in.AllDay, BusyStatus: in.BusyStatus,
			Attendees: in.Attendees, ReminderMin: in.ReminderMin,
		})
		// draftFromInput attaches the host's local zone because Exchange
		// requires one on calendar Add; on an update it would instead
		// overwrite the item's existing zone with the host's. Leave it
		// empty so UpdateEvent's read-modify-write round-trips the raw
		// time-zone blob the server reported (or omits the element, and
		// the server keeps its default).
		draft.TimeZoneRaw = ""
		draft.ClearFields = in.Clear
		if err := c.UpdateEvent(ctx, in.FolderID, in.ID, draft); err != nil {
			return nil, CalendarCreateEventOutput{}, fmt.Errorf("UpdateEvent: %w", err)
		}
		return jsonResult(CalendarCreateEventOutput{ID: in.ID})
	})
}

// CalendarDeleteEventInput is the schema for calendar_delete_event.
type CalendarDeleteEventInput struct {
	Account  string `json:"account"`
	FolderID string `json:"folder_id"`
	ID       string `json:"id"`
}

func registerCalendarDelete(s *mcp.Server, m *Manager, accounts []string) {
	tool := &mcp.Tool{
		Name:        "calendar_delete_event",
		Description: "Delete a calendar event by id.",
	}
	scopeEnum(tool, "account", accounts)
	mcp.AddTool(s, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in CalendarDeleteEventInput) (*mcp.CallToolResult, CalendarCreateEventOutput, error) {
		if err := m.CheckClass(in.Account, config.ClassCalendar, true); err != nil {
			return nil, CalendarCreateEventOutput{}, err
		}
		c, err := m.Client(ctx, in.Account)
		if err != nil {
			return nil, CalendarCreateEventOutput{}, err
		}
		if err := c.DeleteEvent(ctx, in.FolderID, in.ID); err != nil {
			return nil, CalendarCreateEventOutput{}, fmt.Errorf("DeleteEvent: %w", err)
		}
		return jsonResult(CalendarCreateEventOutput{ID: in.ID})
	})
}

// CalendarRespondInviteInput is the schema for calendar_respond_invite.
type CalendarRespondInviteInput struct {
	Account  string `json:"account"`
	FolderID string `json:"folder_id" jsonschema:"folder containing the invitation message"`
	ID       string `json:"id" jsonschema:"server id of the invitation"`
	Response string `json:"response" jsonschema:"accept | tentative | decline"`
}

// CalendarRespondInviteOutput reports the calendar event id created (if any).
type CalendarRespondInviteOutput struct {
	CalendarID string `json:"calendar_id,omitempty"`
	Status     int    `json:"status"`
}

func registerCalendarRespondInvite(s *mcp.Server, m *Manager, accounts []string) {
	tool := &mcp.Tool{
		Name:        "calendar_respond_invite",
		Description: "Respond to a meeting invitation in your inbox: accept, tentative, or decline.",
	}
	scopeEnum(tool, "account", accounts)
	mcp.AddTool(s, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in CalendarRespondInviteInput) (*mcp.CallToolResult, CalendarRespondInviteOutput, error) {
		if err := m.CheckClass(in.Account, config.ClassCalendar, true); err != nil {
			return nil, CalendarRespondInviteOutput{}, err
		}
		choice, err := parseInviteResponse(in.Response)
		if err != nil {
			return nil, CalendarRespondInviteOutput{}, err
		}
		c, err := m.Client(ctx, in.Account)
		if err != nil {
			return nil, CalendarRespondInviteOutput{}, err
		}
		res, err := c.RespondInvite(ctx, in.FolderID, in.ID, choice)
		if err != nil {
			return nil, CalendarRespondInviteOutput{}, fmt.Errorf("RespondInvite: %w", err)
		}
		return jsonResult(CalendarRespondInviteOutput{CalendarID: res.CalendarID, Status: res.Status})
	})
}

func parseInviteResponse(s string) (eas.MeetingResponseChoice, error) {
	switch strings.ToLower(s) {
	case "accept", "1":
		return eas.MeetingAccept, nil
	case "tentative", "2":
		return eas.MeetingTentative, nil
	case "decline", "3":
		return eas.MeetingDecline, nil
	}
	return 0, fmt.Errorf("response must be one of accept|tentative|decline (got %q)", s)
}

func draftFromInput(in CalendarCreateEventInput) eas.EventDraft {
	d := eas.EventDraft{
		Subject:     in.Subject,
		Location:    in.Location,
		Body:        in.Body,
		StartTime:   in.StartTime,
		EndTime:     in.EndTime,
		AllDayEvent: in.AllDay,
		BusyStatus:  in.BusyStatus,
		Reminder:    in.ReminderMin,
		// Exchange requires a TimeZone on calendar Add, and its item
		// converter rejects the all-zero UTC blob with item status 6
		// (ConversionError) — the zero SYSTEMTIME rules are not a valid
		// TIME_ZONE_INFORMATION to Exchange. Use the machine's local
		// zone, which always encodes a real bias and name.
		TimeZoneRaw: eas.EncodeTimeZone(time.Local),
	}
	for _, a := range in.Attendees {
		d.Attendees = append(d.Attendees, eas.EventAttendee{Name: a.Name, Email: a.Email})
	}
	return d
}
