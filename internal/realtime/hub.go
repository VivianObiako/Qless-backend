// Package realtime fans queue changes out to connected browsers.
//
// The hub is in-process, which is what keeps Qless a single binary with no
// broker to run. It also means the API is a single-instance deployment: two
// instances would each hold half the connections and neither would see the
// other's events. Postgres LISTEN/NOTIFY is the path out of that when it
// matters; see the README.
package realtime

import (
	"sync"
)

// Audience decides which encoding of an event a connection receives. The split
// is enforced here, at the socket, rather than by asking callers to remember:
// a connection's audience is settled before the upgrade and cannot change.
//
// Three, not two. Owner and Staff both look at a dashboard, but only the owner
// always sees customer names — staff see them when the queue says so. Calling
// the second one "operator" was fine while the owner was the only operator
// there was.
type Audience string

const (
	Public Audience = "public"
	Owner  Audience = "owner"
	Staff  Audience = "staff"
)

// Event carries every encoding of the same change, already serialised. Encoding
// once per event rather than once per connection matters on the surface most
// likely to have many listeners: a queue where fifty phones are watching.
type Event struct {
	Public []byte
	Owner  []byte
	Staff  []byte
}

// FrameFor is exported for the one caller outside this package that needs it:
// the handshake, which sends an initial snapshot before the client joins a
// room and so never passes through Publish.
func (e Event) FrameFor(audience Audience) []byte {
	switch audience {
	case Owner:
		return e.Owner
	case Staff:
		return e.Staff
	default:
		// Anything that is not explicitly a dashboard is a customer. A new
		// audience added without a frame gets the one with no names in it.
		return e.Public
	}
}

// Hub holds the connections for every live queue, grouped by queue id.
type Hub struct {
	mu    sync.RWMutex
	rooms map[string]map[*Client]struct{}
}

func NewHub() *Hub {
	return &Hub{rooms: make(map[string]map[*Client]struct{})}
}

func (h *Hub) add(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()

	room, ok := h.rooms[c.queueID]
	if !ok {
		room = make(map[*Client]struct{})
		h.rooms[c.queueID] = room
	}
	room[c] = struct{}{}
}

func (h *Hub) remove(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()

	room, ok := h.rooms[c.queueID]
	if !ok {
		return
	}
	delete(room, c)
	if len(room) == 0 {
		delete(h.rooms, c.queueID)
	}
}

// Publish delivers an event to everyone watching one queue, each in their own
// audience's encoding. Delivery is non-blocking: a connection whose buffer has
// filled up is disconnected rather than allowed to stall the queue for
// everybody else. Its browser reconnects and refetches, which is the same
// recovery path as any dropped connection.
func (h *Hub) Publish(queueID string, event Event) {
	h.mu.RLock()
	clients := make([]*Client, 0, len(h.rooms[queueID]))
	for c := range h.rooms[queueID] {
		clients = append(clients, c)
	}
	h.mu.RUnlock()

	for _, c := range clients {
		c.deliver(event.FrameFor(c.audience))
	}
}

// Connections reports how many sockets are watching a queue. Used by the tests
// to wait for a subscription before publishing.
func (h *Hub) Connections(queueID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.rooms[queueID])
}
