# Design: explicit field clearing in `calendar_update_event`

Date: 2026-11-11
Status: approved (design) — pending implementation plan

## Problem

The calendar update path was changed to read-modify-write so that a partial
update no longer wipes fields Exchange would otherwise drop (subject,
attendees, recurrence, time zone). In `mergeEventDraft`, a zero value is the
"leave unchanged" sentinel, which means there is currently **no way to reset a
field back to its default** through the tool — e.g. remove a location link or
remove all attendees.

## Goal

Allow explicitly resetting event fields to their defaults, independent of the
read-modify-write merge.

### Clearable fields

| field | cleared to | notes |
|-------|-----------|-------|
| `location` | empty | common case (removing a meeting link) |
| `attendees` | none | demotes meeting → appointment; Exchange notifies removed attendees |
| `reminder` | 0 (none) | |
| `sensitivity` | 0 (normal) | |
| `busy_status` | 0 (free) | |

### Not clearable

`subject`, `body`, and the start/end time window are not exposed for clearing —
an event must retain a subject and a valid time window. Attempting to clear
them is rejected.

## API shape

A new field on `CalendarUpdateEventInput`:

```go
Clear []string `json:"clear,omitempty"` // field names to reset to default
```

Example call:

```json
{ "account":"work", "folder_id":"23", "id":"23:24", "clear":["location","attendees"] }
```

`clear` may be combined with setting other fields in the same call.

## Implementation

Two layers, both required.

### 1. `eas.EventDraft.ClearFields []string` + `buildEventApp`

Setting a field to zero in the merged draft is **not sufficient**: `buildEventApp`
only serializes non-zero values (`if draft.Location != ""`,
`if len(draft.Attendees) > 0`, `if draft.Reminder > 0`). A zeroed field would be
omitted from the `<ApplicationData>` and the server would keep the old value.

So `buildEventApp` must, for each name in `ClearFields`, emit an **explicitly
empty** element even when the value is zero:

- `location` → `<Location></Location>`
- `attendees` → empty `<Attendees/>` container
- `reminder` → `<Reminder>0</Reminder>`
- `sensitivity` / `busy_status` → always serialized already, so assigning `0`
  sends them without special-casing (but they are still listed as clearable so
  the contract is uniform).

### 2. `mergeEventDraft` applies the clear

After the normal merge, for each name in `draft.ClearFields` force the
corresponding merged field to its zero/default value. The clear wins over any
value seeded from the fetched current state.

### Public interface

`eas.CalendarClient.UpdateEvent` signature is unchanged; `easmock` is untouched.
The clear is carried inside the existing `EventDraft`.

## Error handling (MCP layer, `tools_calendar.go`)

- Unknown name in `clear` → error listing the allowed names.
- Conflict: a field supplied both as a value and in `clear` (e.g.
  `location:"x"` + `clear:["location"]`) → **error**, not silent precedence.
  (`sensitivity` has no value form in the input, so it cannot conflict.)

## Tests

1. `eas`: `TestMergeEventDraftClear` — clearing zeroes the affected fields.
2. `eas`: `TestBuildEventAppEmitsEmptyClearedFields` — the empty elements are
   actually present in the rendered XML.
3. `lib/server`: unknown-name rejection, conflict rejection, successful clear.

## Side effects

Clearing `attendees` removes everyone; Exchange sends the removed attendees a
notification that they were excluded from the meeting (standard protocol
behavior).
