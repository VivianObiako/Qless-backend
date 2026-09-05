package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/vivianobiako/qless/api/internal/httpx"
	"github.com/vivianobiako/qless/api/internal/queue"
	"github.com/vivianobiako/qless/api/internal/storage"
	"github.com/vivianobiako/qless/api/internal/token"
)

// decodeFields re-reads an already-decoded object into a typed struct, so a
// handler can ask which keys the caller actually sent and still get typed
// values. Unknown fields are refused on the way through, exactly as
// httpx.DecodeJSON would have done: a typo in a settings key should fail
// loudly rather than silently leave the setting alone.
func decodeFields[T any](raw map[string]json.RawMessage) (T, error) {
	var out T

	encoded, err := json.Marshal(raw)
	if err != nil {
		return out, err
	}

	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&out); err != nil {
		return out, err
	}
	return out, nil
}

// listEntries backs the operator dashboard. requireQueueAccess runs first, so a
// customer holding only the public slug cannot read customer names — and
// maySeeNames decides whether the staff who *can* open this see them.
func (s *Server) listEntries(w http.ResponseWriter, r *http.Request) {
	q, actor, ok := s.requireQueueAccess(w, r)
	if !ok {
		return
	}

	view, err := s.operatorView(r.Context(), q, maySeeNames(q, actor))
	if err != nil {
		writeError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, view)
}

// serveNext attends the current customer and promotes the next one, returning
// the refreshed dashboard so the operator's screen updates from one request.
//
// Working the counter is shared with operators by the permission table, so this
// asks for access to the queue rather than for ownership of it.
func (s *Server) serveNext(w http.ResponseWriter, r *http.Request) {
	q, actor, ok := s.requireQueueAccess(w, r)
	if !ok {
		return
	}

	result, err := s.store.ServeNext(r.Context(), q.ID, actor)
	if err != nil {
		writeError(w, err)
		return
	}

	// Serving next both closes out one customer and calls another. Every event
	// carries a full snapshot, so rather than broadcast twice we name the fact
	// that matters to whoever is listening: someone new was called, or — when
	// the queue has emptied — the last customer was finished with.
	switch {
	case result.Served != nil:
		s.publish(r.Context(), q.ID, EventCustomerServed)
	case result.Attended != nil && result.Attended.Status == queue.EntrySkipped:
		s.publish(r.Context(), q.ID, EventCustomerSkipped)
	case result.Attended != nil:
		s.publish(r.Context(), q.ID, EventCustomerAttended)
	}

	s.respondWithView(w, r, q.ID, actor)
}

// entryID reads and shape-checks the {entryId} path value, so a malformed id
// answers 404 rather than reaching the driver as a bad uuid.
func entryID(r *http.Request) (string, bool) {
	id := strings.TrimSpace(r.PathValue("entryId"))
	if id == "" || !storage.IsUUID(id) {
		return "", false
	}
	return id, true
}

// serveEntry calls one named customer to the counter, out of order.
func (s *Server) serveEntry(w http.ResponseWriter, r *http.Request) {
	q, actor, ok := s.requireQueueAccess(w, r)
	if !ok {
		return
	}

	id, ok := entryID(r)
	if !ok {
		writeError(w, queue.ErrEntryNotFound)
		return
	}

	if _, err := s.store.ServeEntry(r.Context(), q.ID, id, actor); err != nil {
		writeError(w, err)
		return
	}

	// One broadcast, as with serve next: every event carries a full snapshot,
	// and what matters to a listener is that somebody was called.
	s.publish(r.Context(), q.ID, EventCustomerServed)
	s.respondWithView(w, r, q.ID, actor)
}

// attendEntry closes out one customer as dealt with.
func (s *Server) attendEntry(w http.ResponseWriter, r *http.Request) {
	q, actor, ok := s.requireQueueAccess(w, r)
	if !ok {
		return
	}

	id, ok := entryID(r)
	if !ok {
		writeError(w, queue.ErrEntryNotFound)
		return
	}

	if _, err := s.store.AttendEntry(r.Context(), q.ID, id, actor); err != nil {
		writeError(w, err)
		return
	}

	s.publish(r.Context(), q.ID, EventCustomerAttended)
	s.respondWithView(w, r, q.ID, actor)
}

// startEntry marks that service has begun for the person at the counter, for
// the case nothing inferred it. Nothing about the queue's order changes; the
// counter's clock and the estimate do.
func (s *Server) startEntry(w http.ResponseWriter, r *http.Request) {
	q, actor, ok := s.requireQueueAccess(w, r)
	if !ok {
		return
	}

	id, ok := entryID(r)
	if !ok {
		writeError(w, queue.ErrEntryNotFound)
		return
	}

	if _, err := s.store.StartServing(r.Context(), q.ID, id); err != nil {
		writeError(w, err)
		return
	}

	s.publish(r.Context(), q.ID, EventCustomerStarted)
	s.respondWithView(w, r, q.ID, actor)
}

// skipEntry stands a customer down. They keep their record and can rejoin for a
// fresh number, which is why this is not a deletion.
func (s *Server) skipEntry(w http.ResponseWriter, r *http.Request) {
	q, actor, ok := s.requireQueueAccess(w, r)
	if !ok {
		return
	}

	id, ok := entryID(r)
	if !ok {
		writeError(w, queue.ErrEntryNotFound)
		return
	}

	if _, err := s.store.SkipEntry(r.Context(), q.ID, id, actor); err != nil {
		writeError(w, err)
		return
	}

	s.publish(r.Context(), q.ID, EventCustomerSkipped)
	s.respondWithView(w, r, q.ID, actor)
}

// setStatus backs pause, resume and close. Pausing and resuming are shared with
// operators; closing is the owner's alone, so the caller supplies the check it
// has already run rather than this deciding for itself.
func (s *Server) setStatus(
	w http.ResponseWriter,
	r *http.Request,
	q queue.Queue,
	actor queue.Actor,
	status queue.Status,
	note string,
	event EventType,
) {
	// An archived queue stays closed until it is restored; reopening it from
	// the counter would put a queue back in service that its owner has put
	// away, without the list ever showing it.
	if q.ArchivedAt != nil {
		writeError(w, invalid("This queue is archived. Restore it from your queues first."))
		return
	}

	if _, err := s.store.SetStatus(r.Context(), q.ID, status, note); err != nil {
		writeError(w, err)
		return
	}

	s.publish(r.Context(), q.ID, event)
	s.respondWithView(w, r, q.ID, actor)
}

type pauseRequest struct {
	Note string `json:"note"`
}

const pauseNoteLimit = 80

// pauseQueue stops new joins, with an optional line for the people who
// scan in meanwhile: "Back at 2:30". The body is optional, so a client that
// pauses with no body still pauses.
func (s *Server) pauseQueue(w http.ResponseWriter, r *http.Request) {
	q, actor, ok := s.requireQueueAccess(w, r)
	if !ok {
		return
	}

	var req pauseRequest
	if r.ContentLength != 0 {
		if err := httpx.DecodeJSON(w, r, &req); err != nil {
			writeError(w, invalid("We couldn't read that request."))
			return
		}
	}
	note := strings.TrimSpace(req.Note)
	if len([]rune(note)) > pauseNoteLimit {
		writeError(w, invalid("Keep the note to 80 characters."))
		return
	}

	s.setStatus(w, r, q, actor, queue.StatusPaused, note, EventQueuePaused)
}

func (s *Server) resumeQueue(w http.ResponseWriter, r *http.Request) {
	q, actor, ok := s.requireQueueAccess(w, r)
	if !ok {
		return
	}
	s.setStatus(w, r, q, actor, queue.StatusOpen, "", EventQueueResumed)
}

// closeQueue is owner-only: ending the day is a decision about the business,
// not a counter action.
func (s *Server) closeQueue(w http.ResponseWriter, r *http.Request) {
	q, actor, ok := s.requireOwner(w, r)
	if !ok {
		return
	}
	s.setStatus(w, r, q, actor, queue.StatusClosed, "", EventQueueClosed)
}

// archiveQueue puts a queue away: closed, hidden from the owner's list, and
// refusing joins, with every entry it ever recorded left where it is. It is
// the nearest thing to deleting a queue this product offers, on purpose.
func (s *Server) archiveQueue(w http.ResponseWriter, r *http.Request) {
	s.setArchived(w, r, true)
}

func (s *Server) unarchiveQueue(w http.ResponseWriter, r *http.Request) {
	s.setArchived(w, r, false)
}

func (s *Server) setArchived(w http.ResponseWriter, r *http.Request, archived bool) {
	q, actor, ok := s.requireOwner(w, r)
	if !ok {
		return
	}

	updated, err := s.store.SetArchived(r.Context(), q.ID, archived)
	if err != nil {
		writeError(w, err)
		return
	}

	event := EventQueueUpdated
	if archived {
		event = EventQueueClosed
	}
	s.publish(r.Context(), updated.ID, event)
	s.respondWithView(w, r, updated.ID, actor)
}

// resetQueue clears the line and starts numbering again. Owner-only, and the
// most destructive thing on the dashboard — history survives it, the queue
// does not.
func (s *Server) resetQueue(w http.ResponseWriter, r *http.Request) {
	q, actor, ok := s.requireOwner(w, r)
	if !ok {
		return
	}

	if _, err := s.store.ResetQueue(r.Context(), q.ID); err != nil {
		writeError(w, err)
		return
	}

	s.publish(r.Context(), q.ID, EventQueueReset)
	s.respondWithView(w, r, q.ID, actor)
}

type updateQueueRequest struct {
	Name                  *string `json:"name"`
	Description           *string `json:"description"`
	AverageServiceMinutes *int    `json:"averageServiceMinutes"`
	MaxCapacity           *int    `json:"maxCapacity"`
	ShowNamesToOperators  *bool   `json:"showNamesToOperators"`
	HoldMinutes           *int    `json:"holdMinutes"`
}

const holdMinutesLimit = 120

// validate mirrors the create-time rules. A field the caller did not send is
// left alone; maxCapacity sent as null means "no limit", which is why its
// presence is tracked separately from its value.
func (r updateQueueRequest) validate(capacityPresent bool) (storage.UpdateQueueParams, error) {
	params := storage.UpdateQueueParams{
		AverageServiceMinutes: r.AverageServiceMinutes,
		MaxCapacitySet:        capacityPresent,
		MaxCapacity:           r.MaxCapacity,
		ShowNamesToOperators:  r.ShowNamesToOperators,
		HoldMinutes:           r.HoldMinutes,
	}

	if r.HoldMinutes != nil && (*r.HoldMinutes < 0 || *r.HoldMinutes > holdMinutesLimit) {
		return storage.UpdateQueueParams{}, invalid("Hold time must be between 0 and 120 minutes.")
	}

	if r.Name != nil {
		name := strings.TrimSpace(*r.Name)
		if name == "" {
			return storage.UpdateQueueParams{}, invalid("Enter a name for your queue.")
		}
		if len([]rune(name)) > 80 {
			return storage.UpdateQueueParams{}, invalid("Queue name must be 80 characters or fewer.")
		}
		params.Name = &name
	}

	if r.Description != nil {
		description := strings.TrimSpace(*r.Description)
		if len([]rune(description)) > 200 {
			return storage.UpdateQueueParams{}, invalid("Description must be 200 characters or fewer.")
		}
		params.Description = &description
	}

	if r.AverageServiceMinutes != nil &&
		(*r.AverageServiceMinutes < 1 || *r.AverageServiceMinutes > 480) {
		return storage.UpdateQueueParams{}, invalid("Average service time must be between 1 and 480 minutes.")
	}

	if capacityPresent && r.MaxCapacity != nil && (*r.MaxCapacity < 1 || *r.MaxCapacity > 1000) {
		return storage.UpdateQueueParams{}, invalid("Maximum queue size must be between 1 and 1000.")
	}

	return params, nil
}

// updateQueue changes the queue's settings. Owner-only: an operator runs the
// counter, they do not reconfigure the business.
func (s *Server) updateQueue(w http.ResponseWriter, r *http.Request) {
	q, actor, ok := s.requireOwner(w, r)
	if !ok {
		return
	}

	// Decoded via a map first, on purpose. A *int cannot distinguish
	// "maxCapacity": null from an absent maxCapacity, and those mean opposite
	// things: clear the limit, or leave it exactly as it is.
	var raw map[string]json.RawMessage
	if err := httpx.DecodeJSON(w, r, &raw); err != nil {
		writeError(w, invalid("We couldn't read that request."))
		return
	}
	_, capacityPresent := raw["maxCapacity"]

	req, err := decodeFields[updateQueueRequest](raw)
	if err != nil {
		writeError(w, invalid("We couldn't read that request."))
		return
	}

	params, err := req.validate(capacityPresent)
	if err != nil {
		writeError(w, err)
		return
	}

	updated, err := s.store.UpdateQueue(r.Context(), q.ID, params)
	if err != nil {
		writeError(w, err)
		return
	}

	// Settings change what every screen shows — the name on a customer's
	// ticket, the estimate under it — so this is a queue-wide event.
	s.publish(r.Context(), updated.ID, EventQueueUpdated)
	s.respondWithView(w, r, updated.ID, actor)
}

// The history screen pages through this itself; the cap is what keeps one
// request from carrying a whole year.
const (
	historyDefault = 200
	historyLimit   = 1000
)

type walkInRequest struct {
	Name string `json:"name"`
}

// addWalkIn puts somebody in the queue from the counter: a person with no
// phone, or a phone that will not scan. They get the next number like anyone
// else. The token behind the entry is minted here and thrown away, so nothing
// can recover it on a device — which is the point, and what the flag says.
func (s *Server) addWalkIn(w http.ResponseWriter, r *http.Request) {
	q, actor, ok := s.requireQueueAccess(w, r)
	if !ok {
		return
	}

	var req walkInRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		writeError(w, invalid("We couldn't read that request."))
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeError(w, invalid("Enter their name to add them."))
		return
	}
	if len([]rune(name)) > 60 {
		writeError(w, invalid("Name must be 60 characters or fewer."))
		return
	}

	discarded, err := token.New()
	if err != nil {
		writeError(w, err)
		return
	}

	if _, err := s.store.AddWalkIn(r.Context(), q.ID, name, token.Hash(discarded)); err != nil {
		writeError(w, err)
		return
	}

	s.publish(r.Context(), q.ID, EventCustomerJoined)
	s.respondWithView(w, r, q.ID, actor)
}

// queueHistory returns what this queue has finished with. Names are in here, so
// it sits behind the same check as the dashboard itself.
func (s *Server) queueHistory(w http.ResponseWriter, r *http.Request) {
	q, actor, ok := s.requireQueueAccess(w, r)
	if !ok {
		return
	}

	limit := historyDefault
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > historyLimit {
			writeError(w, invalid("Limit must be a whole number between 1 and 1000."))
			return
		}
		limit = parsed
	}

	entries, err := s.store.History(r.Context(), q.ID, limit)
	if err != nil {
		writeError(w, err)
		return
	}

	// The third surface a customer name can reach, and the easiest to forget:
	// the dashboard and the socket both hide names from staff, and a history
	// screen that did not would hand back everything they had just been kept
	// from. The operator's own name stays — who served whom is staffing, not a
	// customer's privacy.
	withNames := maySeeNames(q, actor)
	if !withNames {
		for i := range entries {
			entries[i].CustomerName = ""
		}
	}

	ownerName, err := s.store.OwnerNameForQueue(r.Context(), q.ID)
	if err != nil {
		writeError(w, err)
		return
	}

	httpx.JSON(w, http.StatusOK, historyResponse{
		Queue:      q,
		Entries:    entries,
		ShowsNames: withNames,
		OwnerName:  ownerName,
	})
}

type historyResponse struct {
	Queue      queue.Queue          `json:"queue"`
	Entries    []queue.HistoryEntry `json:"entries"`
	ShowsNames bool                 `json:"showsNames"`

	// OwnerName lets an entry the owner handled carry their name rather than
	// "the owner". Empty when they have not given one.
	OwnerName string `json:"ownerName"`
}

// respondWithView answers every operator action with the refreshed dashboard,
// so a screen updates from one round trip instead of waiting for its own
// broadcast to come back.
//
// The queue row is re-read rather than taken from the handler, for the same
// reason buildEvent re-reads it: the copy resolved at the top of the request is
// stale the moment the handler changes anything. Pausing answered "OPEN" and
// resetting answered with the old next number until this stopped trusting it.
func (s *Server) respondWithView(
	w http.ResponseWriter,
	r *http.Request,
	queueID string,
	actor queue.Actor,
) {
	q, err := s.store.GetQueue(r.Context(), queueID)
	if err != nil {
		writeError(w, err)
		return
	}

	view, err := s.operatorView(r.Context(), q, maySeeNames(q, actor))
	if err != nil {
		writeError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, view)
}
