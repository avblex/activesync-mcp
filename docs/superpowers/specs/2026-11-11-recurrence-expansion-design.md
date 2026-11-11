# Design: expanding recurring calendar series in `calendar_list_events`

Date: 2026-11-11
Status: approved (design) — pending implementation plan

## Problem

Exchange ActiveSync does not expand recurring series on its own. A calendar
`Sync` returns **one anchor element per series** — the item carrying the
series' first occurrence date and a `<Recurrence>` rule — regardless of how
long the series runs. Clients are expected to compute the occurrences
themselves.

Today `calendar_list_events` drops that information entirely:
`eventRowFrom()` (`lib/server/tools_calendar.go`) never copies
`eas.EventItem.Recurrence` into `EventRow`, so an LLM client sees the anchor
with its original (possibly years-old) date and no way to know the event also
happens tomorrow. Observed consequence: asked "what do I have tomorrow?", the
assistant answered "free day" while a weekly meeting («Еженедельный статус
(производство)») was actually scheduled.

## Goal

Make `calendar_list_events` return concrete occurrences of recurring series
within the requested window, so date-based questions ("what's on tomorrow")
are answerable from the tool output alone.

## Decisions (agreed)

- **Approach:** generator lives in the vendored EAS layer (option A), next to
  the types it operates on. The MCP layer calls it.
- **Series types:** all six supported by `eas.Recurrence` — daily, weekly,
  monthly-by-date, monthly-by-day, yearly-by-date, yearly-by-day.
- **Exceptions:** both kinds supported — deleted occurrences skipped; modified
  occurrences carry the overridden fields.
- **Output shape:** one row per occurrence, each with a distinct `id`
  (`<server-id>@<YYYY-MM-DD>`) plus a new `series_id` field holding the
  original server id. Single (non-recurring) events keep their plain `id` and
  no `series_id`.
- **Window:** occurrences are generated only for the requested window
  `[today, today + date_window)`. No look-back into the past.
- **No cap** on occurrences per series within the window.
- **`date_window=none`:** infinite series are expanded with a **1-year
  horizon** from today; finite series stop at their own `Until`/`Occurrences`
  if earlier.

## Architecture

### New file: `vendor/github.com/hstern/go-activesync/eas/recurrence_expand.go`

```go
// ExpandRecurrences returns one EventItem per occurrence of ev's series that
// starts within [windowStart, windowEnd), with exceptions applied. For a
// non-recurring item it returns nil (callers keep the original row).
func ExpandRecurrences(ev EventItem, windowStart, windowEnd time.Time) []EventItem
```

Two stateless helpers inside:

- `nextOccurrence(cur time.Time, r *Recurrence) (time.Time, bool)` — the next
  occurrence after `cur`; `false` when the series has ended (`Until` reached
  or `Occurrences` exhausted).
- `applyException(item EventItem, x Exception) EventItem` — applies a
  modification exception's overrides.

Generation walks **forward from the anchor** (`ev.StartTime`) via
`nextOccurrence` until past `windowEnd`. Deterministic, independent of
"now". A hard iteration ceiling (~10 000) guards against malformed rules with
a zero step; normally the window closes generation long before that.

### MCP layer changes (`lib/server/tools_calendar.go`)

- `EventRow` gains `SeriesID string \`json:"series_id,omitempty"\``.
- In `registerCalendarListEvents`, items with `Recurrence != nil` are passed
  through `eas.ExpandRecurrences(...)`; each returned occurrence becomes a row
  with `ID = serverID + "@" + <occurrence start, UTC YYYY-MM-DD>` and
  `SeriesID = serverID`. Non-recurring items flow through `eventRowFrom`
  unchanged.
- Expansion window: `[today, today + date_window)` — the same window the
  server is asked about. For `date_window=none` the right edge is
  `today + 1 year` (infinite series); finite series still stop at their own
  end if earlier.
- Tool description documents `series_id` and that update/delete take the
  anchor id (the bare server id), not an occurrence id.

Other tools (`calendar_get_event`, update, delete) are **not changed**: they
operate on the anchor id, which Exchange accepts for series-wide operations.

## Occurrence generation rules

Base rule: candidate = current occurrence advanced by the type-specific step;
stop when the candidate is past `Until` (if set) or the occurrence counter is
exhausted.

| Type | Step | Notes |
|------|------|-------|
| Daily (0) | `+Interval` days | |
| Weekly (1) | `+7·Interval` weeks, snapped to `DayOfWeek` mask | Multi-day masks (e.g. Mon+Thu) iterate the days ascending within the series week; the anchor sets the phase. |
| MonthlyDate (2) | `+Interval` months, day = `DayOfMonth` | Short month: a 31st in a 30-day month is **skipped**, not clamped (matches Outlook/Exchange). |
| MonthlyByDay (3) | `+Interval` months, day = `WeekOfMonth`-th `DayOfWeek` | `WeekOfMonth=5` → last such day of the month. |
| YearlyDate (5) | `+Interval` years, date = `MonthOfYear`/`DayOfMonth` | Feb 29 in a non-leap year is skipped. |
| YearlyByDay (6) | `+Interval` years, day = `WeekOfMonth`-th `DayOfWeek` in `MonthOfYear` | |

Invariants:

- **Time-of-day is fixed** from the anchor `StartTime`; only the date part
  advances. All occurrences share the same clock time.
- **Absolute dates.** Everything is computed in UTC as delivered by the
  server; no local-zone or DST conversion, so occurrence dates are
  deterministic.
- An occurrence is included iff its start lies in `[windowStart, windowEnd)`.
- If the anchor date itself falls outside the window, it is not shown as a
  separate row — only in-window occurrences appear.

## Exceptions

Applied per generated occurrence on exact `ExceptionStartTime` match:

- `Deleted=true` → occurrence dropped from the result.
- Modified occurrence → fields overridden from the exception, only when
  present/non-zero: `Subject`, `Location`, `Body`, `BusyStatus>0`,
  `Reminder>0`; times only when **both** `StartTime` and `EndTime` are set
  (otherwise the computed time stands). Covers the common "moved to another
  slot" case.

## Error handling

The generator returns no errors. A malformed rule (zero interval, unknown
type) simply yields no occurrences; the item then appears as its anchor row
(degraded, not fatal). No new error codes; a list request never fails because
of one odd series.

## Testing

EAS layer (`recurrence_expand_test.go`):

1. Each recurrence type produces the expected occurrence dates across a
   window (table-driven, fixed anchor + window).
2. Short-month skip (MonthlyDate 31st) and non-leap Feb 29 (YearlyDate).
3. `Until` and `Occurrences` termination; infinite series bounded by the
   window.
4. Deleted exception removes the matching occurrence; modified exception
   overrides subject/location/time (and leaves computed time when only one
   side is given).
5. Anchor outside the window → no spurious anchor row; multi-day weekly mask
   order.
6. Zero-step / unknown-type rule → empty result, no panic.

MCP layer (`tools_calendar_test.go`):

7. A recurring item in the sync result expands into multiple rows with
   correct `id` suffixes and `series_id`; single events are untouched.
8. `date_window=none` expansion respects the 1-year horizon for an infinite
   series.

## Out of scope

- Look-back into past occurrences (window is forward-only).
- Changing update/delete semantics for individual occurrences (anchor id
  stays the unit of operation).
- Server-side expansion or new EAS commands.
