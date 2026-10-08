package meshpath

import (
	"time"

	meshcore "github.com/OwlShack/meshcore-go"
	"github.com/OwlShack/meshcore-go/node"
)

// Send queues a packet we built at the firmware's priority for its route and type, a flood scoped to scope.
func Send(n *node.Node, pkt *meshcore.Packet, scope *meshcore.Region, delay time.Duration) error {
	if pkt.IsRouteDirect() {
		return n.SendDirect(pkt, pkt.Path, pkt.PathHashSize(), delay)
	}
	return n.SendFlood(pkt, scope, pkt.PathHashSize(), delay)
}
