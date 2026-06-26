package hub

// room is a single named chat room's membership set. It is owned exclusively by the
// hub goroutine, so it needs no mutex — all access happens on the actor (NFR-C2).
type room struct {
	// name is the room's identifier, unique within the hub.
	name string
	// members is the set of clients currently in the room. struct{} values make it a
	// set with zero per-entry storage (CODING-STANDARDS §5).
	members map[*Client]struct{}
}

// newRoom returns an empty room with the given name.
func newRoom(name string) *room {
	return &room{name: name, members: make(map[*Client]struct{})}
}

// add puts c into the room. Adding an existing member is a no-op.
func (r *room) add(c *Client) {
	r.members[c] = struct{}{}
}

// remove drops c from the room. Removing a non-member is a no-op.
func (r *room) remove(c *Client) {
	delete(r.members, c)
}

// empty reports whether the room has no members, so the hub can reclaim it.
func (r *room) empty() bool {
	return len(r.members) == 0
}

// names returns the display names of current members, for presence frames (FR-9).
func (r *room) names() []string {
	out := make([]string, 0, len(r.members))
	for c := range r.members {
		out = append(out, c.Name)
	}
	return out
}
