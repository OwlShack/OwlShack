package repeater

import "errors"

// The ways a request to a remote node fails before or instead of an answer; each wraps the message the operator sees.
var (
	ErrBadPubkey     = errors.New("invalid pubkey")
	ErrUnknownPeer   = errors.New("peer not found in peer table")
	ErrNotLoggedIn   = errors.New("not logged in")
	ErrNotAdmin      = errors.New("CLI requires an admin session; the node ignores commands from other roles")
	ErrNoReply       = errors.New("the node did not answer")
	ErrBusy          = errors.New("too many CLI commands in flight")
	ErrNoDirectRoute = errors.New("no direct route to the room yet: it ignores flooded keep-alives, so log in to learn one")
	ErrRejected      = errors.New("rejected")
	ErrBadReply      = errors.New("the node's reply could not be read")
)
