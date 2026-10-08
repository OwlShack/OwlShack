package app

import (
	"context"
	"encoding/hex"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/OwlShack/OwlShack/internal/api"
	"github.com/OwlShack/OwlShack/internal/config"
	"github.com/OwlShack/OwlShack/internal/store"
	meshcore "github.com/OwlShack/meshcore-go"
	"github.com/OwlShack/meshcore-go/node"
)

// floodLabels is the regions received packets are named from, swapped whenever the config is applied.
var floodLabels atomic.Pointer[[]*meshcore.Region]

func setFloodLabels(cfg *config.Config) {
	regions := cfg.LabelRegions()
	floodLabels.Store(&regions)
}

// floodScopeOf names the region a received packet carried; see config.PacketScope.
func floodScopeOf(pkt *meshcore.Packet) string {
	var regions []*meshcore.Region
	if p := floodLabels.Load(); p != nil {
		regions = *p
	}
	return config.PacketScope(pkt, regions)
}

// TX is hooked on the modem, not a virtual radio: a virtual radio fires outbound handlers only for its own sends.
func wirePacketLogger(mux *node.RadioMux, modem node.Modem, db *store.Store, srv *api.Server, compReg *companionRegistry) {
	hub := srv.Hub()
	logRadio := mux.NewRadio()

	logRadio.SetRawDataHandler(func(data []byte, snr float32, rssi int8, hasSignalInfo bool) {
		radioSeen.rx()
		pkt, err := meshcore.PacketFromBytes(data)
		routeType, payloadType := packetTypes(pkt, err)

		var snrPtr *float64
		var rssiPtr *int8
		if hasSignalInfo {
			s := float64(snr)
			snrPtr = &s
			rssiPtr = &rssi
		}

		rec := &store.PacketRecord{
			ReceivedAt:  time.Now(),
			Direction:   "rx",
			Raw:         data,
			RouteType:   routeType,
			PayloadType: payloadType,
			SNR:         snrPtr,
			RSSI:        rssiPtr,
		}
		if err == nil {
			rec.PacketHash, rec.Path = store.PacketFieldsFromPkt(pkt)
		}

		db.WriteAsync(func() {
			if insertErr := db.Packets.Insert(context.Background(), rec); insertErr != nil {
				slog.Debug("failed to log rx packet", "error", insertErr)
			}
		})

		msg := packetBroadcastMsg("rx", rec.ReceivedAt, data, pkt, err, srv.ChannelLookup())
		if hasSignalInfo {
			msg["snr"] = snr
			msg["rssi"] = rssi
		}
		hub.Broadcast("packets", msg)
	})

	modem.AddOutboundHandler(func(data []byte) {
		radioSeen.tx()
		pkt, err := meshcore.PacketFromBytes(data)
		routeType, payloadType := packetTypes(pkt, err)

		rec := &store.PacketRecord{
			ReceivedAt:  time.Now(),
			Direction:   "tx",
			Raw:         data,
			RouteType:   routeType,
			PayloadType: payloadType,
		}
		if err == nil {
			rec.PacketHash, rec.Path = store.PacketFieldsFromPkt(pkt)
		}

		db.WriteAsync(func() {
			if insertErr := db.Packets.Insert(context.Background(), rec); insertErr != nil {
				slog.Debug("failed to log tx packet", "error", insertErr)
			}
		})

		hub.Broadcast("packets", packetBroadcastMsg("tx", rec.ReceivedAt, data, pkt, err, srv.ChannelLookup()))

		// Resolved through the registry because a reload builds new observers while this handler can never be removed.
		if compReg != nil {
			for _, c := range compReg.all() {
				if obs := c.Observer(); obs != nil {
					obs.NoteTx(data)
				}
			}
		}
	})
}

// packetPruneLoop prunes at startup and hourly, re-reading the setting each pass so a save needs no reload.
func packetPruneLoop(ctx context.Context, db *store.Store) {
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	for {
		prunePackets(ctx, db)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// prunePackets deletes one batch per writer turn so a long backlog never fills the queue and drops RX writes.
func prunePackets(ctx context.Context, db *store.Store) {
	days, err := db.Settings.PacketRetentionDays(ctx)
	if err != nil {
		slog.Warn("packet prune skipped", "error", err)
		return
	}
	cutoff := time.Now().AddDate(0, 0, -days)
	const batch = 500
	for ctx.Err() == nil {
		var n int64
		db.WriteSync(func() {
			var err error
			if n, err = db.Packets.PruneBatchBefore(ctx, cutoff, batch); err != nil {
				slog.Warn("packet prune failed", "error", err)
			}
		})
		if n < batch {
			return
		}
	}
}

// packetTypes returns (nil, nil) when parsing failed.
func packetTypes(pkt *meshcore.Packet, parseErr error) (routeType, payloadType *uint8) {
	if parseErr != nil {
		return nil, nil
	}
	rt := pkt.RouteType()
	pt := pkt.PayloadType()
	return &rt, &pt
}

func packetBroadcastMsg(direction string, receivedAt time.Time, data []byte, pkt *meshcore.Packet, parseErr error, channels api.ChannelLookup) map[string]any {
	msg := map[string]any{
		"direction":  direction,
		"receivedAt": receivedAt.Format(api.TimestampLayout), // sub-second: the packets UI orders observations by this
		"raw":        hex.EncodeToString(data),
	}
	if parseErr != nil {
		return msg
	}
	msg["routeType"] = pkt.RouteType()
	msg["payloadType"] = pkt.PayloadType()
	msg["route"] = pkt.RouteTypeString()
	msg["pathHashSize"] = pkt.PathHashSize()
	msg["hops"] = pkt.PathHashCount()
	msg["packetHash"], msg["path"] = store.PacketFieldsFromPkt(pkt)
	msg["summary"] = api.PacketSummary(pkt, channels)
	msg["floodScope"] = floodScopeOf(pkt)
	return msg
}
