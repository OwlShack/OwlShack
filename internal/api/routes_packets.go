package api

import (
	"encoding/hex"
	"net/http"
	"strconv"

	"github.com/OwlShack/OwlShack/internal/store"
	meshcore "github.com/OwlShack/meshcore-go"
)

// TimestampLayout is RFC 3339 with exactly three fractional digits. Packet observations are
// ordered by this on the client, so the fraction has to be there — but ECMAScript defines only
// three digits and treats more as implementation-defined, and this ships as a PWA on phones, so
// RFC3339Nano's variable 0-9 digits would be a bet on every viewer's engine.
const TimestampLayout = "2006-01-02T15:04:05.000Z07:00"

func (s *Server) handleListPackets(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	if limit <= 0 {
		limit = 100
	}

	var filter store.PacketFilter
	if pt := r.URL.Query().Get("payloadType"); pt != "" {
		if n, err := strconv.ParseUint(pt, 10, 8); err == nil {
			v := uint8(n)
			filter.PayloadType = &v
		}
	}
	filter.Search = r.URL.Query().Get("q")

	packets, err := s.store.Packets.List(r.Context(), limit, offset, filter)
	if err != nil {
		s.serverError(w, "failed to list packets", err)
		return
	}

	type packetJSON struct {
		ID           int64    `json:"id"`
		ReceivedAt   string   `json:"receivedAt"`
		Direction    string   `json:"direction"`
		Raw          string   `json:"raw"`
		RouteType    *uint8   `json:"routeType,omitempty"`
		PayloadType  *uint8   `json:"payloadType,omitempty"`
		Route        string   `json:"route,omitempty"`
		PathHashSize *uint8   `json:"pathHashSize,omitempty"`
		Hops         *uint8   `json:"hops,omitempty"`
		Path         string   `json:"path,omitempty"`
		PacketHash   string   `json:"packetHash,omitempty"`
		Summary      string   `json:"summary,omitempty"`
		SNR          *float64 `json:"snr,omitempty"`
		RSSI         *int8    `json:"rssi,omitempty"`
	}

	out := make([]packetJSON, 0, len(packets))
	for _, p := range packets {
		j := packetJSON{
			ID:          p.ID,
			ReceivedAt:  p.ReceivedAt.UTC().Format(TimestampLayout),
			Direction:   p.Direction,
			Raw:         hex.EncodeToString(p.Raw),
			RouteType:   p.RouteType,
			PayloadType: p.PayloadType,
			SNR:         p.SNR,
			RSSI:        p.RSSI,
		}

		if pkt, err := meshcore.PacketFromBytes(p.Raw); err == nil {
			j.Route = pkt.RouteTypeString()
			phs := pkt.PathHashSize()
			phc := pkt.PathHashCount()
			j.PathHashSize = &phs
			j.Hops = &phc
			j.PacketHash, j.Path = store.PacketFieldsFromPkt(pkt)
			j.Summary = PacketSummary(pkt, s.ChannelLookup())
		}

		out = append(out, j)
	}
	writeJSON(w, http.StatusOK, out)
}
