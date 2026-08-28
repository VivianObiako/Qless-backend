package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/vivianobiako/qless/api/internal/queue"
	"github.com/vivianobiako/qless/api/internal/realtime"
)

// EventType names what changed. Every event carries a full snapshot rather than
// a delta, so a browser that misses or reorders a frame still lands on the
// right state — the type is there to let the UI react, not to reconstruct.
type EventType string

const (
	EventQueueUpdated     EventType = "QUEUE_UPDATED"
	EventCustomerJoined   EventType = "CUSTOMER_JOINED"
	EventCustomerLeft     EventType = "CUSTOMER_LEFT"
	EventCustomerSkipped  EventType = "CUSTOMER_SKIPPED"
	EventCustomerServed   EventType = "CUSTOMER_SERVED"
	EventCustomerAttended EventType = "CUSTOMER_ATTENDED"
	EventQueuePaused      EventType = "QUEUE_PAUSED"
	EventQueueResumed     EventType = "QUEUE_RESUMED"
	EventQueueClosed      EventType = "QUEUE_CLOSED"
	EventQueueReset       EventType = "QUEUE_RESET"
)

// PublicEvent is what customer phones and display screens receive. It carries
// the same state as the public HTTP response and, like it, no names.
type PublicEvent struct {
	Type  EventType         `json:"type"`
	At    time.Time         `json:"at"`
	State queue.PublicState `json:"state"`
}

// OperatorEvent goes only to connections that proved they run this queue. It is
// the one frame on the wire that can carry customer names — whether it does
// depends on which dashboard audience it was built for.
type OperatorEvent struct {
	Type EventType    `json:"type"`
	At   time.Time    `json:"at"`
	View OperatorView `json:"view"`
}

// buildEvent renders every audience's view of the queue as it stands now.
//
// The queue row is re-read rather than taken from the caller: a join advances
// next_number and an operator action can change status, so the copy the handler
// resolved at the top of the request is already stale by the time we broadcast.
func (s *Server) buildEvent(ctx context.Context, queueID string, eventType EventType) (realtime.Event, error) {
	q, err := s.store.GetQueue(ctx, queueID)
	if err != nil {
		return realtime.Event{}, err
	}

	state, err := s.store.PublicState(ctx, q)
	if err != nil {
		return realtime.Event{}, err
	}

	at := time.Now().UTC()

	publicFrame, err := json.Marshal(PublicEvent{Type: eventType, At: at, State: state})
	if err != nil {
		return realtime.Event{}, err
	}

	ownerView, err := s.operatorView(ctx, q, true)
	if err != nil {
		return realtime.Event{}, err
	}
	ownerFrame, err := json.Marshal(OperatorEvent{Type: eventType, At: at, View: ownerView})
	if err != nil {
		return realtime.Event{}, err
	}

	// With names on, staff see exactly what the owner sees, so the same bytes
	// go to both. With names off the staff frame is built separately and is the
	// only one of the three that has to be constructed to leave something out.
	staffFrame := ownerFrame
	if !q.ShowNamesToOperators {
		staffView, err := s.operatorView(ctx, q, false)
		if err != nil {
			return realtime.Event{}, err
		}
		staffFrame, err = json.Marshal(OperatorEvent{Type: eventType, At: at, View: staffView})
		if err != nil {
			return realtime.Event{}, err
		}
	}

	return realtime.Event{Public: publicFrame, Owner: ownerFrame, Staff: staffFrame}, nil
}

// publish broadcasts a change to everyone watching this queue.
//
// A broadcast that fails is logged and swallowed. The mutation it describes has
// already been committed, and failing the customer's request because a fan-out
// query went wrong would turn a stale screen into a lost place in the queue.
func (s *Server) publish(ctx context.Context, queueID string, eventType EventType) {
	event, err := s.buildEvent(ctx, queueID, eventType)
	if err != nil {
		slog.Error("build realtime event", "error", err, "queue", queueID, "event", eventType)
		return
	}
	s.hub.Publish(queueID, event)
}
