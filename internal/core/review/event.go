package review

import (
	"encoding/json"
	"time"
)

// Event is one line of the index. Fields carries whichever record fields this
// event sets, under the Record JSON names.
type Event struct {
	ID     string          `json:"id"`
	At     time.Time       `json:"at"`
	Type   string          `json:"type"`
	Fields json.RawMessage `json:"fields,omitempty"`
}

const (
	EventPrepared   = "prepared"
	EventStarted    = "started"
	EventDrafted    = "drafted"
	EventSubmitted  = "submitted"
	EventArchived   = "archived"
	EventAbandoned  = "abandoned"
	EventUnreviewed = "unreviewed"
	EventReviewed   = "reviewed"
	EventNotStarted = "not_started"
)

var eventStates = map[string]State{
	EventPrepared:   StatePreparing,
	EventStarted:    StateReviewing,
	EventDrafted:    StateDrafted,
	EventSubmitted:  StateSubmitted,
	EventArchived:   StateArchived,
	EventAbandoned:  StateAbandoned,
	EventUnreviewed: StateUnreviewed,
	EventReviewed:   StateReviewed,
	EventNotStarted: StateNotStarted,
}

// stateEvents is the inverse of eventStates, spelled out rather than derived. Two
// event types that share one state then collide visibly here, instead of letting
// map iteration pick an arbitrary winner.
var stateEvents = map[State]string{
	StatePreparing:  EventPrepared,
	StateReviewing:  EventStarted,
	StateDrafted:    EventDrafted,
	StateSubmitted:  EventSubmitted,
	StateArchived:   EventArchived,
	StateAbandoned:  EventAbandoned,
	StateUnreviewed: EventUnreviewed,
	StateReviewed:   EventReviewed,
	StateNotStarted: EventNotStarted,
}

// KnownEvent reports whether Fold understands this event's type. Compaction asks,
// because it rewrites the log from what Fold produced: an event Fold skipped is
// gone from disk afterwards. An event with no ID never reaches a record, so it
// does not count as unknown and cannot disable compaction on its own.
func KnownEvent(e Event) bool {
	if e.ID == "" {
		return true
	}
	_, ok := eventStates[e.Type]
	return ok
}

// Fold replays the log into one record per ID, in order of first appearance. An
// event sets only the fields it carries, so later events leave the rest intact.
// docket's own events carry the whole record, which makes each one a replacement.
// An event with an unknown type changes nothing, which keeps a log written by a
// newer docket readable here.
func Fold(events []Event) []Record {
	byID := make(map[string]*Record, len(events))
	order := make([]string, 0, len(events))

	for _, e := range events {
		if e.ID == "" {
			continue
		}
		state, known := eventStates[e.Type]
		if !known {
			continue
		}
		rec, seen := byID[e.ID]
		if !seen {
			rec = &Record{ID: e.ID}
			byID[e.ID] = rec
			order = append(order, e.ID)
		}
		if len(e.Fields) > 0 {
			// Decoding onto the record already in hand is what makes an event a
			// patch. An absent JSON key leaves its field untouched.
			_ = json.Unmarshal(e.Fields, rec)
		}
		rec.ID = e.ID
		rec.State = state
	}

	records := make([]Record, 0, len(order))
	for _, id := range order {
		records = append(records, *byID[id])
	}
	return records
}

// EventForState returns the event type that records a given state, which is how
// compaction writes one snapshot event per record.
func EventForState(s State) (string, bool) {
	eventType, ok := stateEvents[s]
	return eventType, ok
}
