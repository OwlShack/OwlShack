package app

import (
	"context"
	"encoding/hex"
	"log/slog"
	"time"

	"github.com/meshcore-go/OwlShack/internal/api"
	"github.com/meshcore-go/OwlShack/internal/store"
	meshcore "github.com/meshcore-go/meshcore-go"
	"github.com/meshcore-go/meshcore-go/node"
)

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
	cutoff := time.Now().AddDate(0, 0, -db.Settings.PacketRetentionDays(ctx))
	for more := true; more && ctx.Err() == nil; {
		more = false
		db.WriteSync(func() {
			var err error
			if more, err = db.Packets.PruneBatchBefore(ctx, cutoff, 500); err != nil {
				slog.Warn("packet prune failed", "error", err)
			}
		})
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
	return msg
}
