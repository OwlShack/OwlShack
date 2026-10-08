// Package advert builds and sends MeshCore self-adverts for the node personalities that announce themselves.
package advert

import (
	"log/slog"
	"math"
	"time"

	meshcore "github.com/OwlShack/meshcore-go"
	"github.com/OwlShack/meshcore-go/node"

	"github.com/OwlShack/OwlShack/internal/meshpath"
)

// floodPathLength encodes the path-hash width in BYTES as (size-1) in PathLength's top 2 bits; clamped, a 0 would underflow to 0xC0.
func floodPathLength(pathHashSize int) byte {
	if pathHashSize < 1 || pathHashSize > 3 {
		pathHashSize = 1
	}
	return meshcore.MakePathLen(uint8(pathHashSize), 0)
}

// SendSelf transmits a self-advert: flood is mesh-wide, otherwise zero-hop to direct neighbours only; scope wraps a flood in a transport region.
func SendSelf(n *node.Node, log *slog.Logger, advType, name string, lat, lon *float64, flood bool, pathHashSize int, scope *meshcore.Region) error {
	appData := meshcore.AdvertAppData{Type: advType, Name: name}
	if lat != nil && lon != nil && (*lat != 0 || *lon != 0) {
		appData.Lat = int32(math.Round(*lat * 1_000_000.0))
		appData.Lon = int32(math.Round(*lon * 1_000_000.0))
	}
	rawAppData, err := appData.ToBytes()
	if err != nil {
		return err
	}

	adv := meshcore.Advert{
		PublicKey:  n.Identity().Identity,
		Timestamp:  uint32(time.Now().Unix()),
		RawAppData: rawAppData,
	}
	// SignWith, not Sign(PrivateKey()): an imported expanded-key identity has no usable seed.
	adv.SignWith(n.Identity())

	payload, err := adv.ToBytes()
	if err != nil {
		return err
	}

	// The firmware reads a direct packet with path_len==0 as zero-hop: accepted by neighbours, never relayed.
	routeType := meshcore.RouteTypeFlood
	pathLength := floodPathLength(pathHashSize)
	if !flood {
		routeType = meshcore.RouteTypeDirect
		pathLength = 0
	}

	pkt := &meshcore.Packet{
		Header:     meshcore.MakeHeader(routeType, meshcore.PayloadTypeAdvert, 0),
		PathLength: pathLength,
		Payload:    payload,
	}
	if flood {
		pkt.SetScope(scope) // firmware sendFloodScoped(default_scope, ...); nil stays unscoped
	}

	mode := "flood"
	if !flood {
		mode = "zero-hop"
	} else if scope != nil {
		mode += " scoped"
	}
	log.Info("sending self-advert", "mode", mode)

	return meshpath.Send(n, pkt, scope, 0)
}
