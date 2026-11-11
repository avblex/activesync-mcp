# Recurring-Series Expansion Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `calendar_list_events` return concrete occurrences of recurring series within the requested window instead of dropping the recurrence rule.

**Architecture:** A new pure generator in the vendored EAS layer (`eas.ExpandRecurrences`) walks forward from a series' anchor occurrence and emits one `EventItem` per in-window occurrence with exceptions applied. The MCP layer calls it for items with `Recurrence != nil`, renders each occurrence as its own row with an `id` suffix and a new `series_id` field, and falls back to the plain anchor row when expansion yields nothing.

**Tech Stack:** Go 1.x, vendored `github.com/hstern/go-activesync v1.1.0` (edited in-place), MCP SDK tests via `easmock`.

**Spec:** `docs/superpowers/specs/2026-11-11-recurrence-expansion-design.md`

---

## Environment notes (read first)

- Work from `/mnt/c/Users/abelsky/activesync-mcp`, branch `fix_state_db`.
- **CRITICAL:** `go build`, `go vet`, and `go test` FAIL inside the default Bash sandbox with `cannot find module providing package ... import lookup disabled by -mod=vendor`. Run every such command with `dangerouslyDisableSandbox: true`. `gofmt` works fine in-sandbox.
- Vendored files live under `vendor/github.com/hstern/go-activesync/eas/`. Editing them in-place is the established pattern in this repo (see commits `4eb775d`, `ad6bf0d`).
- Existing types used throughout (already present, do not modify):

```go
// vendor/github.com/hstern/go-activesync/eas/recurrence.go
type RecurrenceType int
const (
	RecurrenceDaily        RecurrenceType = 0
	RecurrenceWeekly       RecurrenceType = 1
	RecurrenceMonthlyDate  RecurrenceType = 2
	RecurrenceMonthlyByDay RecurrenceType = 3
	RecurrenceYearlyDate   RecurrenceType = 5
	RecurrenceYearlyByDay  RecurrenceType = 6
)
type DayOfWeek int // bitmask: DowSunday=1 ... DowSaturday=64, DowLastDay=127
type Recurrence struct {
	Type           RecurrenceType
	Interval       int       // every N days/weeks/months; <=0 treated as 1
	Until          time.Time // zero = forever
	Occurrences    int       // alternative end; 0 = unset
	DayOfWeek      DayOfWeek
	DayOfMonth     int
	WeekOfMonth    int // 1-5, 5 = "last"
	MonthOfYear    int // 1-12
	IsLeapMonth    bool
	FirstDayOfWeek int
	CalendarType   int
}
type Exception struct {
	ExceptionStartTime time.Time
	Deleted            bool
	Subject, Location  string
	StartTime, EndTime time.Time
	AllDayEvent        bool
	BusyStatus, Reminder int
	Body               string
}
// EventItem fields relevant here: ServerID, UID, Subject, Location, Body,
// StartTime, EndTime, AllDayEvent, BusyStatus, Sensitivity, MeetingStatus,
// Reminder, OrganizerName, OrganizerEmail, Attendees []EventAttendee,
// Recurrence *Recurrence, Exceptions []Exception, TimeZone, TimeZoneRaw.
```

- File structure for this feature:
  - Create `vendor/github.com/hstern/go-activesync/eas/recurrence_expand.go` — the generator (Task 1).
  - Create `vendor/github.com/hstern/go-activesync/eas/recurrence_expand_test.go` — generator tests (Task 1).
  - Modify `lib/server/tools_calendar.go` — `EventRow.SeriesID`, list handler wiring, tool description (Task 2).
  - Modify `lib/server/tools_calendar_test.go` — MCP-layer tests (Task 2).

---

### Task 1: `eas.ExpandRecurrences` generator

**Files:**
- Create: `vendor/github.com/hstern/go-activesync/eas/recurrence_expand.go`
- Test: `vendor/github.com/hstern/go-activesync/eas/recurrence_expand_test.go`

- [ ] **Step 1: Write the failing tests**

Create `vendor/github.com/hstern/go-activesync/eas/recurrence_expand_test.go`:

```go
// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"testing"
	"time"
)

func mustDate(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse("2006-01-02", s)
	if err != nil {
		t.Fatalf("bad date %q: %v", s, err)
	}
	return ts
}

// weeklySeries builds a weekly series anchored on Thursday 2025-10-10 07:30Z.
func weeklySeries(until time.Time) EventItem {
	return EventItem{
		ServerID:  "23:24",
		Subject:   "Еженедельный статус (производство)",
		StartTime: mustDate(t, "2025-10-10").Add(7*time.Hour + 30*time.Minute),
		EndTime:   mustDate(t, "2025-10-10").Add(9*time.Hour),
		Recurrence: &Recurrence{
			Type:      RecurrenceWeekly,
			Interval:  1,
			DayOfWeek: DowThursday,
			Until:     until,
		},
	}
}

func TestExpandRecurrences_weeklyInWindow(t *testing.T) {
	ev := weeklySeries(time.Time{}) // infinite
	got := ExpandRecurrences(ev, mustDate(t, "2026-11-11"), mustDate(t, "2026-11-18"))
	// Thursdays 12 and 16 Nov 2026 fall in [11, 18); 11th is Wednesday.
	want := []string{"2026-11-12", "2026-11-16"}
	if len(got) != len(want) {
		t.Fatalf("got %d occurrences, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if d := got[i].StartTime.Format("2006-01-02"); d != w {
			t.Errorf("occurrence %d start = %s, want %s", i, d, w)
		}
		if !got[i].StartTime.Equal(mustDate(t, w).Add(7*time.Hour + 30*time.Minute)) {
			t.Errorf("occurrence %d time-of-day drifted: %v", i, got[i].StartTime)
		}
		if got[i].ServerID != "23:24" || got[i].Subject != ev.Subject {
			t.Errorf("occurrence %d lost item fields: %+v", i, got[i])
		}
	}
}

func TestExpandRecurrences_untilStopsSeries(t *testing.T) {
	ev := weeklySeries(mustDate(t, "2026-11-12")) // ends on the 12th
	got := ExpandRecurrences(ev, mustDate(t, "2026-11-01"), mustDate(t, "2026-12-31"))
	if len(got) != 1 || got[0].StartTime.Format("2006-01-02") != "2026-11-12" {
		t.Fatalf("want exactly the 12th, got %+v", got)
	}
}

func TestExpandRecurrences_occurrencesLimit(t *testing.T) {
	ev := EventItem{
		ServerID:  "f:1",
		Subject:   "daily",
		StartTime: mustDate(t, "2026-11-01").Add(9 * time.Hour),
		EndTime:   mustDate(t, "2026-11-01").Add(10 * time.Hour),
		Recurrence: &Recurrence{
			Type:        RecurrenceDaily,
			Interval:    1,
			Occurrences: 3, // 1st, 2nd, 3rd Nov only
		},
	}
	got := ExpandRecurrences(ev, mustDate(t, "2026-10-01"), mustDate(t, "2026-12-31"))
	if len(got) != 3 {
		t.Fatalf("want 3 occurrences, got %d", len(got))
	}
	if got[2].StartTime.Format("2006-01-02") != "2026-11-03" {
		t.Errorf("last occurrence = %s, want 2026-11-03", got[2].StartTime.Format("2006-01-02"))
	}
}

func TestExpandRecurrences_deletedAndModifiedExceptions(t *testing.T) {
	ev := weeklySeries(time.Time{})
	ev.Exceptions = []Exception{
		{ExceptionStartTime: mustDate(t, "2026-11-12").Add(7*time.Hour + 30*time.Minute), Deleted: true},
		{
			ExceptionStartTime: mustDate(t, "2026-11-16").Add(7*time.Hour + 30*time.Minute),
			Subject:            "Статус: перенос",
			Location:           "Zoom (новое)",
		},
	}
	got := ExpandRecurrences(ev, mustDate(t, "2026-11-11"), mustDate(t, "2026-11-18"))
	if len(got) != 1 {
		t.Fatalf("want 1 occurrence (12th deleted), got %d", len(got))
	}
	if got[0].Subject != "Статус: перенос" || got[0].Location != "Zoom (новое)" {
		t.Errorf("modified exception not applied: %+v", got[0])
	}
}

func TestExpandRecurrences_modifiedTimeOnlyWhenBothSides(t *testing.T) {
	ev := weeklySeries(time.Time{})
	base := mustDate(t, "2026-11-12").Add(7*time.Hour + 30*time.Minute)
	// Only a new Start given → keep the computed window.
	ev.Exceptions = []Exception{{ExceptionStartTime: base, StartTime: base.Add(-time.Hour)}}
	got := ExpandRecurrences(ev, mustDate(t, "2026-11-11"), mustDate(t, "2026-11-13"))
	if len(got) != 1 || !got[0].StartTime.Equal(base) {
		t.Fatalf("single-sided time override must be ignored, got %+v", got)
	}
	// Both sides → applied.
	ev.Exceptions = []Exception{{ExceptionStartTime: base, StartTime: base.Add(-time.Hour), EndTime: base.Add(-time.Hour + 90 * time.Minute)}}
	got = ExpandRecurrences(ev, mustDate(t, "2026-11-11"), mustDate(t, "2026-11-13"))
	if len(got) != 1 || !got[0].StartTime.Equal(base.Add(-time.Hour)) || !got[0].EndTime.Equal(base.Add(-time.Hour+90*time.Minute)) {
		t.Fatalf("both-sided time override not applied, got %+v", got)
	}
}

func TestExpandRecurrences_monthlyDateSkipsShortMonths(t *testing.T) {
	// Every 31st: Jan 31, Mar 31, May 31, Jul 31, Aug 31, Oct 31, Dec 31 —
	// Feb/Apr/Jun/Sep/Nov have no 31st and are skipped, not clamped.
	ev := EventItem{
		ServerID:  "f:2",
		StartTime: mustDate(t, "2026-01-31").Add(10 * time.Hour),
		EndTime:   mustDate(t, "2026-01-31").Add(11 * time.Hour),
		Recurrence: &Recurrence{
			Type:       RecurrenceMonthlyDate,
			Interval:   1,
			DayOfMonth: 31,
		},
	}
	got := ExpandRecurrences(ev, mustDate(t, "2026-01-01"), mustDate(t, "2027-01-01"))
	var dates []string
	for _, g := range got {
		dates = append(dates, g.StartTime.Format("2006-01-02"))
	}
	want := []string{"2026-01-31", "2026-03-31", "2026-05-31", "2026-07-31", "2026-08-31", "2026-10-31", "2026-12-31"}
	if len(dates) != len(want) {
		t.Fatalf("got %v, want %v", dates, want)
	}
	for i := range want {
		if dates[i] != want[i] {
			t.Errorf("date %d = %s, want %s", i, dates[i], want[i])
		}
	}
}

func TestExpandRecurrences_monthlyByDayLast(t *testing.T) {
	// Last Friday of each month.
	ev := EventItem{
		ServerID:  "f:3",
		StartTime: mustDate(t, "2026-11-27").Add(15 * time.Hour), // Fri
		EndTime:   mustDate(t, "2026-11-27").Add(16 * time.Hour),
		Recurrence: &Recurrence{
			Type:        RecurrenceMonthlyByDay,
			Interval:    1,
			DayOfWeek:   DowFriday,
			WeekOfMonth: 5,
		},
	}
	got := ExpandRecurrences(ev, mustDate(t, "2026-11-01"), mustDate(t, "2027-02-01"))
	var dates []string
	for _, g := range got {
		dates = append(dates, g.StartTime.Format("2006-01-02"))
	}
	// 2026: Nov 27, Dec 25; 2027: Jan 29, Feb 26.
	want := []string{"2026-11-27", "2026-12-25", "2027-01-29", "2027-02-26"}
	if len(dates) != len(want) {
		t.Fatalf("got %v, want %v", dates, want)
	}
	for i := range want {
		if dates[i] != want[i] {
			t.Errorf("date %d = %s, want %s", i, dates[i], want[i])
		}
	}
}

func TestExpandRecurrences_yearlyFeb29SkippedNonLeap(t *testing.T) {
	ev := EventItem{
		ServerID:  "f:4",
		StartTime: mustDate(t, "2024-02-29").Add(8 * time.Hour),
		EndTime:   mustDate(t, "2024-02-29").Add(9 * time.Hour),
		Recurrence: &Recurrence{
			Type:        RecurrenceYearlyDate,
			Interval:    1,
			MonthOfYear: 2,
			DayOfMonth:  29,
		},
	}
	got := ExpandRecurrences(ev, mustDate(t, "2024-01-01"), mustDate(t, "2033-01-01"))
	var dates []string
	for _, g := range got {
		dates = append(dates, g.StartTime.Format("2006-01-02"))
	}
	// Leap years only: 2024, 2028, 2032.
	want := []string{"2024-02-29", "2028-02-29", "2032-02-29"}
	if len(dates) != len(want) {
		t.Fatalf("got %v, want %v", dates, want)
	}
	for i := range want {
		if dates[i] != want[i] {
			t.Errorf("date %d = %s, want %s", i, dates[i], want[i])
		}
	}
}

func TestExpandRecurrences_multiDayWeeklyMask(t *testing.T) {
	// Mon+Thu weekly, anchored Monday 2026-11-09.
	ev := EventItem{
		ServerID:  "f:5",
		StartTime: mustDate(t, "2026-11-09").Add(10 * time.Hour),
		EndTime:   mustDate(t, "2026-11-09").Add(11 * time.Hour),
		Recurrence: &Recurrence{
			Type:      RecurrenceWeekly,
			Interval:  1,
			DayOfWeek: DowMonday | DowThursday,
		},
	}
	got := ExpandRecurrences(ev, mustDate(t, "2026-11-09"), mustDate(t, "2026-11-23"))
	var dates []string
	for _, g := range got {
		dates = append(dates, g.StartTime.Format("2006-01-02"))
	}
	want := []string{"2026-11-09", "2026-11-12", "2026-11-16", "2026-11-19"}
	if len(dates) != len(want) {
		t.Fatalf("got %v, want %v", dates, want)
	}
	for i := range want {
		if dates[i] != want[i] {
			t.Errorf("date %d = %s, want %s", i, dates[i], want[i])
		}
	}
}

func TestExpandRecurrences_anchorOutsideWindowNotShown(t *testing.T) {
	ev := weeklySeries(time.Time{}) // anchored 2025-10-10
	got := ExpandRecurrences(ev, mustDate(t, "2026-11-11"), mustDate(t, "2026-11-18"))
	for _, g := range got {
		if g.StartTime.Before(mustDate(t, "2026-11-11")) {
			t.Fatalf("anchor leaked into results: %+v", g)
		}
	}
}

func TestExpandRecurrences_malformedRuleYieldsNothing(t *testing.T) {
	cases := []EventItem{
		{ServerID: "a", StartTime: mustDate(t, "2026-11-01"), Recurrence: &Recurrence{Type: RecurrenceDaily}}, // zero interval
		{ServerID: "b", StartTime: mustDate(t, "2026-11-01"), Recurrence: &Recurrence{Type: 99}},             // unknown type
	}
	for _, ev := range cases {
		if got := ExpandRecurrences(ev, mustDate(t, "2026-11-01"), mustDate(t, "2026-12-01")); len(got) != 0 {
			t.Errorf("%s: malformed rule produced %d occurrences, want 0", ev.ServerID, len(got))
		}
	}
}

func TestExpandRecurrences_nonRecurringReturnsNil(t *testing.T) {
	ev := EventItem{ServerID: "x", StartTime: mustDate(t, "2026-11-12")}
	if got := ExpandRecurrences(ev, mustDate(t, "2026-11-01"), mustDate(t, "2026-12-01")); got != nil {
		t.Fatalf("non-recurring item must yield nil, got %+v", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./vendor/github.com/hstern/go-activesync/eas/ -run 'TestExpandRecurrences' -v` (with `dangerouslyDisableSandbox: true`)
Expected: compile error — `undefined: ExpandRecurrences`.

- [ ] **Step 3: Implement the generator**

Create `vendor/github.com/hstern/go-activesync/eas/recurrence_expand.go`:

```go
// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"time"
)

// maxOccurrenceIterations guards against malformed rules whose step never
// advances (zero interval, contradictory fields). Normal series stop at the
// window edge or their Until/Occurrences long before this.
const maxOccurrenceIterations = 10000

// weekEpoch is a known-Sunday midnight used to bucket calendar dates into
// absolute week numbers for the weekly rule. (1970-01-04 was a Sunday.)
var weekEpoch = time.Date(1970, 1, 4, 0, 0, 0, 0, time.UTC)

// weekIndex returns the absolute week number containing d (date part only,
// UTC). Two dates share a week iff their indices are equal.
func weekIndex(d time.Time) int {
	dd := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC)
	return int(dd.Sub(weekEpoch).Hours() / 168)
}

// ExpandRecurrences returns one EventItem per occurrence of ev's series that
// starts within [windowStart, windowEnd), with exceptions applied. For a
// non-recurring item it returns nil (callers keep the original row).
//
// Occurrences are generated forward from the anchor (ev.StartTime); the
// anchor itself is included only if it lies inside the window. Time-of-day is
// fixed from the anchor; only the date part advances. Everything is computed
// in absolute (UTC) time — no local-zone or DST conversion.
//
// The function never errors: a malformed rule simply yields no occurrences.
func ExpandRecurrences(ev EventItem, windowStart, windowEnd time.Time) []EventItem {
	if ev.Recurrence == nil {
		return nil
	}
	r := ev.Recurrence
	if r.Interval <= 0 {
		return nil
	}

	exc := map[time.Time]Exception{}
	for _, x := range ev.Exceptions {
		if !x.ExceptionStartTime.IsZero() {
			exc[x.ExceptionStartTime] = x
		}
	}

	var out []EventItem
	anchor := ev.StartTime
	cur := anchor
	count := 0
	for count < maxOccurrenceIterations {
		count++
		if cur.After(windowEnd) {
			break
		}
		if !cur.Before(windowStart) {
			item := ev
			if x, ok := exc[cur]; ok {
				if x.Deleted {
					next, alive := nextOccurrence(cur, anchor, r, count)
					if !alive {
						break
					}
					cur = next
					continue
				}
				item = applyException(item, x)
			}
			out = append(out, item)
		}
		next, alive := nextOccurrence(cur, anchor, r, count)
		if !alive {
			break
		}
		cur = next
	}
	return out
}

// nextOccurrence returns the next occurrence after cur for rule r. The
// second result is false when the series has ended (Until passed or the
// Occurrences budget exhausted). anchor is the series' first occurrence,
// needed by the weekly rule to keep its phase; count is the number of
// occurrences seen so far including cur, used to enforce the Occurrences
// limit.
func nextOccurrence(cur, anchor time.Time, r *Recurrence, count int) (time.Time, bool) {
	// Occurrences counts instances of the series starting with the anchor,
	// so the (N+1)-th instance exists only while count+1 <= Occurrences.
	if r.Occurrences > 0 && count >= r.Occurrences {
		return time.Time{}, false
	}
	next, ok := advanceDate(cur, anchor, r)
	if !ok {
		return time.Time{}, false
	}
	if !r.Until.IsZero() && next.After(r.Until) {
		return time.Time{}, false
	}
	return next, true
}

// advanceDate computes the next occurrence date after cur, keeping the
// anchor's time-of-day. It returns false for rules it cannot satisfy (unknown
// type, empty day mask, missing month/day fields).
func advanceDate(cur, anchor time.Time, r *Recurrence) (time.Time, bool) {
	switch r.Type {
	case RecurrenceDaily:
		return cur.AddDate(0, 0, r.stepDays()), true
	case RecurrenceWeekly:
		return nextWeeklyDay(cur, anchor, r)
	case RecurrenceMonthlyDate:
		return nextMonthlyDate(cur, r)
	case RecurrenceMonthlyByDay:
		return nthDayOfMonthAfter(cur, r.WeekOfMonth, r.DayOfWeek, r.Interval)
	case RecurrenceYearlyDate:
		return nextYearlyDate(cur, r)
	case RecurrenceYearlyByDay:
		return nthDayInMonthAfterYear(cur, r.MonthOfYear, r.WeekOfMonth, r.DayOfWeek, r.Interval)
	default:
		return time.Time{}, false
	}
}

// stepDays normalizes the interval to a positive day-step for daily rules.
func (r *Recurrence) stepDays() int {
	if r.Interval <= 0 {
		return 1
	}
	return r.Interval
}

// nextWeeklyDay finds the next masked weekday strictly after cur, honoring
// the Interval-week phase fixed by the anchor. The series lives on an
// absolute week grid: a calendar week participates iff its index differs
// from the anchor week's index by a multiple of Interval. Within each
// participating week every masked weekday is an occurrence (ascending). This
// handles multi-day masks (e.g. Mon+Thu) correctly — a naive "+7*Interval"
// jump would skip same-week days.
func nextWeeklyDay(cur, anchor time.Time, r *Recurrence) (time.Time, bool) {
	mask := r.DayOfWeek
	if mask == 0 {
		return time.Time{}, false
	}
	interval := r.stepDays()
	tod := cur.Sub(time.Date(cur.Year(), cur.Month(), cur.Day(), 0, 0, 0, 0, time.UTC))
	curWeek := weekIndex(cur)
	anchorWeek := weekIndex(anchor)
	// Start from the current week (same-week masked days after cur still
	// belong to the series); snap forward to the next grid week if the
	// current week does not participate.
	w := curWeek
	delta := w - anchorWeek
	if delta%interval != 0 {
		w += interval - delta%interval
	}
	for ; ; w += interval {
		sunday := weekEpoch.AddDate(0, 0, 7*w) // Sunday opening week w
		for i := 0; i < 7; i++ {
			d := sunday.AddDate(0, 0, i).Add(tod)
			if !dowMatches(d, mask) {
				continue
			}
			if d.After(cur) {
				return d, true
			}
		}
	}
}

// dowMatches reports whether d's weekday is in the mask.
func dowMatches(d time.Time, mask DayOfWeek) bool {
	var bit DayOfWeek
	switch d.Weekday() {
	case time.Sunday:
		bit = DowSunday
	case time.Monday:
		bit = DowMonday
	case time.Tuesday:
		bit = DowTuesday
	case time.Wednesday:
		bit = DowWednesday
	case time.Thursday:
		bit = DowThursday
	case time.Friday:
		bit = DowFriday
	case time.Saturday:
		bit = DowSaturday
	}
	return mask&bit != 0
}

// nextMonthlyDate advances to the same DayOfMonth Interval months later. If
// the target month lacks that day (e.g. the 31st in a 30-day month) the
// occurrence is skipped entirely — the next candidate is another Interval
// months further — matching Outlook/Exchange behaviour rather than clamping
// to month-end. Candidates are enumerated by month and checked against the
// month's actual length (day 0 of the following month), so a clamped date can
// never wedge the arithmetic: for "every 31st" the series runs
// Jan 31 → Mar 31 → May 31 → …, silently dropping Feb/Apr/Jun/Sep/Nov.
func nextMonthlyDate(cur time.Time, r *Recurrence) (time.Time, bool) {
	if r.DayOfMonth < 1 {
		return time.Time{}, false
	}
	interval := r.stepDays()
	tod := cur.Sub(time.Date(cur.Year(), cur.Month(), cur.Day(), 0, 0, 0, 0, time.UTC))
	y, m, _ := cur.Date()
	for i := 0; i < 24*interval+12; i++ {
		m += interval
		yy, mm := yearMonth(y, m)
		dim := time.Date(yy, mm+1, 0, 0, 0, 0, 0, time.UTC).Day() // days in month mm
		if r.DayOfMonth <= dim {
			return time.Date(yy, mm, r.DayOfMonth, 0, 0, 0, 0, time.UTC).Add(tod), true
		}
	}
	return time.Time{}, false
}

// nthDayOfMonthAfter returns the WeekOfMonth-th DayOfWeek day of the month
// Interval months after cur's month (WeekOfMonth 5 = last). cur itself is
// never returned.
func nthDayOfMonthAfter(cur time.Time, weekOfMonth int, dow DayOfWeek, interval int) (time.Time, bool) {
	if dow == 0 || (weekOfMonth < 1 && weekOfMonth != 5) {
		return time.Time{}, false
	}
	if interval <= 0 {
		interval = 1
	}
	y, m, _ := cur.Date()
	m += interval
	for y2, m2 := yearMonth(y, m); ; y2, m2 = yearMonth(y2, m2+1) {
		if d, ok := nthDayOfMonth(y2, m2, weekOfMonth, dow); ok {
			if d.After(cur) {
				return d, true
			}
		}
	}
}

// nthDayInMonthAfterYear is the yearly analogue: WeekOfMonth-th DayOfWeek of
// MonthOfYear, Interval years after cur's year.
func nthDayInMonthAfterYear(cur time.Time, monthOfYear, weekOfMonth int, dow DayOfWeek, interval int) (time.Time, bool) {
	if monthOfYear < 1 || monthOfYear > 12 || dow == 0 || (weekOfMonth < 1 && weekOfMonth != 5) {
		return time.Time{}, false
	}
	if interval <= 0 {
		interval = 1
	}
	y, _, _ := cur.Date()
	for y += interval; ; y += interval {
		if d, ok := nthDayOfMonth(y, time.Month(monthOfYear), weekOfMonth, dow); ok {
			if d.After(cur) {
				return d, true
			}
		}
	}
}

// nthDayOfMonth finds the n-th (or last, n==5) day of the week `dow` in the
// given month/year. All times are midnight UTC so comparisons are date-based.
func nthDayOfMonth(year int, month time.Month, n int, dow DayOfWeek) (time.Time, bool) {
	first := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
	last := first.AddDate(0, 1, 0).AddDate(0, 0, -1)
	var found []time.Time
	for d := first; !d.After(last); d = d.AddDate(0, 0, 1) {
		if dowMatches(d, dow) {
			found = append(found, d)
		}
	}
	if len(found) == 0 {
		return time.Time{}, false
	}
	if n == 5 {
		return found[len(found)-1], true
	}
	if n < 1 || n > len(found) {
		return time.Time{}, false
	}
	return found[n-1], true
}

// nextYearlyDate advances to MonthOfYear/DayOfMonth Interval years later,
// skipping years where the date does not exist (Feb 29 in non-leap years).
func nextYearlyDate(cur time.Time, r *Recurrence) (time.Time, bool) {
	if r.MonthOfYear < 1 || r.MonthOfYear > 12 || r.DayOfMonth < 1 {
		return time.Time{}, false
	}
	for i := 0; i < 8; i++ {
		y, _, _ := cur.Date()
		cand := time.Date(y+r.stepDays()*(i+1), time.Month(r.MonthOfYear), r.DayOfMonth, 0, 0, 0, 0, time.UTC)
		if cand.Day() == r.DayOfMonth && cand.Month() == time.Month(r.MonthOfYear) {
			return cand, true
		}
	}
	return time.Time{}, false
}

// yearMonth normalizes a possibly out-of-range month into (year, month).
func yearMonth(year int, month int) (int, int) {
	for month < 1 {
		month += 12
		year--
	}
	for month > 12 {
		month -= 12
		year++
	}
	return year, month
}

// applyException overrides an occurrence's fields from a modification
// exception. Only present values win: text fields when non-empty, numeric
// fields when non-zero, and the time window only when BOTH sides are given.
func applyException(item EventItem, x Exception) EventItem {
	if x.Subject != "" {
		item.Subject = x.Subject
	}
	if x.Location != "" {
		item.Location = x.Location
	}
	if x.Body != "" {
		item.Body = x.Body
	}
	if x.BusyStatus != 0 {
		item.BusyStatus = x.BusyStatus
	}
	if x.Reminder != 0 {
		item.Reminder = x.Reminder
	}
	if x.AllDayEvent {
		item.AllDayEvent = true
	}
	if !x.StartTime.IsZero() && !x.EndTime.IsZero() {
		item.StartTime = x.StartTime
		item.EndTime = x.EndTime
	}
	return item
}
```

Notes on the implementation choices (keep them as written):
- `nextOccurrence` takes `count` (occurrences seen so far, including the current one) so the `Occurrences` limit means "the series has N instances total, anchor = instance 1".
- `nextWeeklyDay` scans up to 7 days ahead of the `+7·Interval` jump; for a single-day mask the first candidate matches immediately.
- `nextMonthlyDate` / `nextYearlyDate` detect clamping by comparing the resulting day/month against the requested ones and step again instead of accepting a clamped date.
- `nthDayOfMonth` uses `n==5` as "last" per the EAS convention already documented on `Recurrence.WeekOfMonth`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./vendor/github.com/hstern/go-activesync/eas/ -run 'TestExpandRecurrences' -v` (with `dangerouslyDisableSandbox: true`)
Expected: all PASS.

Then run the whole eas package to check for regressions:
Run: `go test ./vendor/github.com/hstern/go-activesync/eas/` (with `dangerouslyDisableSandbox: true`)
Expected: `ok github.com/hstern/go-activesync/eas`.

- [ ] **Step 5: gofmt check**

Run: `gofmt -l vendor/github.com/hstern/go-activesync/eas/recurrence_expand.go vendor/github.com/hstern/go-activesync/eas/recurrence_expand_test.go`
Expected: no output.

- [ ] **Step 6: Commit**

```bash
git add vendor/github.com/hstern/go-activesync/eas/recurrence_expand.go vendor/github.com/hstern/go-activesync/eas/recurrence_expand_test.go
git commit -m "feat(eas): expand recurring series into in-window occurrences"
```

---

### Task 2: MCP layer — `series_id`, list wiring, tool docs

**Files:**
- Modify: `lib/server/tools_calendar.go`
- Test: `lib/server/tools_calendar_test.go`

Context you need:
- `EventRow` (in `tools_calendar.go`) currently has fields ID, Subject, Location, StartTime, EndTime, AllDay, BusyStatus, Organizer, Attendees.
- `eventRowFrom(ev eas.EventItem) EventRow` maps an item to a row.
- `registerCalendarListEvents` builds rows with `out.Events = append(out.Events, eventRowFrom(ev))` for both `res.Added` and `res.Changed`.
- `calendarDateWindow(s)` returns `(eas.FilterType, applied string, broadened bool)`; `"none"` maps to `eas.FilterNone`.
- The handler receives `in CalendarListEventsInput{Account, FolderID, Cursor, DateWindow, WindowSize}`.
- Tests use `newMockManager(t, mock, mockManagerOpts{access: config.AccessRW})`, `calendarMockManager(t, mock)`, `callTool(t, s, name, args)`, and stub `easmock.CalendarClient.SyncCalendarFunc`. Look at `TestCalendarListEvents` for the exact shape of `eas.CalendarSyncResult` fixtures.

- [ ] **Step 1: Write the failing tests**

Append to `lib/server/tools_calendar_test.go`:

```go
// TestCalendarListEvents_expandsRecurringSeries confirms a recurring item is
// returned as one row per in-window occurrence with id suffixes and a
// series_id, while single events pass through untouched.
func TestCalendarListEvents_expandsRecurringSeries(t *testing.T) {
	mock := &easmock.Client{
		CalendarClient: easmock.CalendarClient{
			SyncCalendarFunc: func(_ context.Context, _ string, _ eas.CalendarSyncOptions) (*eas.CalendarSyncResult, error) {
				anchor := time.Date(2025, 10, 10, 7, 30, 0, 0, time.UTC)
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
	// Default window is 2w from today (2026-11-11): the weekly series
	// contributes its Thursday occurrences in [today, today+14d), plus the
	// one-off. Exact count depends on "today"; assert structural facts.
	type row struct {
		ID       string `json:"id"`
		Subject  string `json:"subject"`
		SeriesID string `json:"series_id"`
	}
	var rows []row
	for _, e := range events {
		var r row
		b, _ := json.Marshal(e)
		_ = json.Unmarshal(b, &r)
		rows = append(rows, r)
	}
	var seriesRows, oneOff int
	for _, r := range rows {
		switch {
		case r.SeriesID == "cal-id:42":
			seriesRows++
			if r.ID != "cal-id:42@"+strings.TrimPrefix(r.ID, "cal-id:42@") {
				t.Errorf("series row id %q lacks the @suffix", r.ID)
			}
		case r.ID == "cal-id:7":
			oneOff++
			if r.SeriesID != "" {
				t.Errorf("one-off row carries series_id %q", r.SeriesID)
			}
		}
	}
	if seriesRows == 0 {
		t.Error("no expanded series rows in output")
	}
	if oneOff != 1 {
		t.Errorf("one-off rows = %d, want 1", oneOff)
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
```

Note: the test file needs `"encoding/json"` imported (add it if absent; `strings` is already imported).

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./lib/server/ -run 'TestCalendarListEvents_expands|TestCalendarListEvents_recurringAnchor' -v` (with `dangerouslyDisableSandbox: true`)
Expected: FAIL — the expanding test finds no `series_id` rows (the recurring item appears once as a plain anchor row); the fallback test may already pass trivially, which is fine — the expanding test is the red one.

- [ ] **Step 3: Add `SeriesID` to `EventRow`**

In `lib/server/tools_calendar.go`, extend `EventRow`:

```go
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
```

- [ ] **Step 4: Wire expansion into `registerCalendarListEvents`**

Replace the two row-building loops in the handler with a helper call. Add the helper near `eventRowFrom`:

```go
// expansionWindow derives the client-side expansion window from the
// requested date window. The right edge mirrors what the server was asked
// about; "none" (FilterNone) has no server-side bound, so infinite series
// get a fixed one-year horizon.
func expansionWindow(filter eas.FilterType, now time.Time) (time.Time, time.Time) {
	start := now.Truncate(24 * time.Hour)
	var end time.Time
	switch filter {
	case eas.FilterOneDay:
		end = start.Add(1 * 24 * time.Hour)
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
```

In `registerCalendarListEvents`, replace:

```go
	for _, ev := range res.Added {
		out.Events = append(out.Events, eventRowFrom(ev))
	}
	for _, ev := range res.Changed {
		out.Events = append(out.Events, eventRowFrom(ev))
	}
```

with:

```go
	winStart, winEnd := expansionWindow(filter, time.Now())
	for _, ev := range res.Added {
		out.Events = append(out.Events, eventRowsFrom(ev, winStart, winEnd)...)
	}
	for _, ev := range res.Changed {
		out.Events = append(out.Events, eventRowsFrom(ev, winStart, winEnd)...)
	}
```

(`filter` is already available in the handler from `calendarDateWindow(in.DateWindow)`.)

- [ ] **Step 5: Document `series_id` in the tool description**

Update the `calendar_list_events` tool Description to mention expansion:

```go
	Description: "List calendar events from a folder. " +
		"Use date_window to limit recency. Recurring series are expanded " +
		"into one row per occurrence within the window; such rows carry a " +
		"series_id (the bare id to use for update/delete) and an id " +
		"suffixed with the occurrence date.",
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./lib/server/ -run 'TestCalendarListEvents' -v` (with `dangerouslyDisableSandbox: true`)
Expected: all PASS.

**Step 7: Run the whole lib/server package to check for regressions**

Run: `go test ./lib/server/` (with `dangerouslyDisableSandbox: true`)
Expected: `ok activesync-mcp/lib/server`.

- [ ] **Step 8: gofmt check**

Run: `gofmt -l lib/server/tools_calendar.go lib/server/tools_calendar_test.go`
Expected: no output.

- [ ] **Step 9: Commit**

```bash
git add lib/server/tools_calendar.go lib/server/tools_calendar_test.go
git commit -m "feat(calendar): expand recurring series in calendar_list_events"
```

---

### Task 3: Full verification

**Files:** none (verification only).

- [ ] **Step 1: Build everything**

Run: `go build ./...` (with `dangerouslyDisableSandbox: true`)
Expected: success, no output.

- [ ] **Step 2: Vet the touched packages**

Run: `go vet ./lib/server/ ./vendor/github.com/hstern/go-activesync/eas/` (with `dangerouslyDisableSandbox: true`)
Expected: clean.

- [ ] **Step 3: Run both affected test suites**

Run: `go test ./vendor/github.com/hstern/go-activesync/eas/ ./lib/server/ -count=1` (with `dangerouslyDisableSandbox: true`)
Expected: both `ok`.

- [ ] **Step 4: gofmt all changed files**

Run: `gofmt -l vendor/github.com/hstern/go-activesync/eas/recurrence_expand.go vendor/github.com/hstern/go-activesync/eas/recurrence_expand_test.go lib/server/tools_calendar.go lib/server/tools_calendar_test.go`
Expected: no output.

- [ ] **Step 5: Confirm scope**

Run: `git status --short` and `git diff --stat HEAD~2..HEAD`
Expected: only the four files from Tasks 1–2 appear (plus any pre-existing untracked files like `.probe/`, `state.db` — those are unrelated and must NOT be committed).
