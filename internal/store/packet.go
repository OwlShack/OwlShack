package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	meshcore "github.com/meshcore-go/meshcore-go"
)

// PacketFieldsFromPkt is the single derivation of the hex packet hash and hop path, so stored, broadcast and displayed forms cannot drift.
func PacketFieldsFromPkt(pkt *meshcore.Packet) (packetHash, path string) {
	h := pkt.PacketHash()
	return hex.EncodeToString(h[:]), hex.EncodeToString(pkt.Path)
}

// derivePacketFields is the raw-bytes form of PacketFieldsFromPkt; unparseable bytes yield empty strings.
func derivePacketFields(raw []byte) (packetHash, path string) {
	pkt, err := meshcore.PacketFromBytes(raw)
	if err != nil {
		return "", ""
	}
	return PacketFieldsFromPkt(pkt)
}

// PacketFilter narrows a packet listing. The zero value lists everything.
type PacketFilter struct {
	PayloadType *uint8 // nil = any type
	Search      string // substring match against packet_hash or path (hex); "" = no search
}

type PacketRecord struct {
	ID          int64
	ReceivedAt  time.Time
	Direction   string
	Raw         []byte
	RouteType   *uint8
	PayloadType *uint8
	SNR         *float64
	RSSI        *int8
	// Set these via PacketFieldsFromPkt to avoid a re-parse; Insert derives them from Raw when both are empty.
	PacketHash string
	Path       string
}

type PacketRepo struct {
	db *sql.DB
}

func (r *PacketRepo) Insert(ctx context.Context, p *PacketRecord) error {
	packetHash, path := p.PacketHash, p.Path
	if packetHash == "" && path == "" {
		packetHash, path = derivePacketFields(p.Raw)
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO packets (received_at, direction, raw, route_type, payload_type, snr, rssi, packet_hash, path)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ReceivedAt.UnixMilli(), p.Direction, p.Raw, p.RouteType, p.PayloadType, p.SNR, p.RSSI, packetHash, path,
	)
	if err != nil {
		return fmt.Errorf("inserting packet: %w", err)
	}
	return nil
}

func (r *PacketRepo) List(ctx context.Context, limit, offset int, filter PacketFilter) ([]PacketRecord, error) {
	if limit <= 0 {
		limit = 100
	}

	where := ""
	var args []any
	if filter.PayloadType != nil {
		where += " AND payload_type = ?"
		args = append(args, *filter.PayloadType)
	}
	if q := strings.ToLower(strings.TrimSpace(filter.Search)); q != "" {
		// packet_hash and path are lowercase hex, so a lowercased needle makes instr() case-insensitive.
		where += " AND (instr(packet_hash, ?) > 0 OR instr(path, ?) > 0)"
		args = append(args, q, q)
	}
	args = append(args, limit, offset)

	rows, err := r.db.QueryContext(ctx, `
		SELECT id, received_at, direction, raw, route_type, payload_type, snr, rssi
		FROM packets
		WHERE 1=1`+where+`
		ORDER BY id DESC
		LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("querying packets: %w", err)
	}
	defer rows.Close()

	var packets []PacketRecord
	for rows.Next() {
		var p PacketRecord
		if err := rows.Scan(
			&p.ID, unixMS(&p.ReceivedAt), &p.Direction, &p.Raw,
			&p.RouteType, &p.PayloadType, &p.SNR, &p.RSSI,
		); err != nil {
			return nil, fmt.Errorf("scanning packet row: %w", err)
		}
		packets = append(packets, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating packets: %w", err)
	}
	return packets, nil
}

func (r *PacketRepo) Count(ctx context.Context) (int64, error) {
	var count int64
	err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM packets").Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting packets: %w", err)
	}
	return count, nil
}

// PruneBatchBefore deletes up to batch of the packets received before cutoff and reports how many went.
func (r *PacketRepo) PruneBatchBefore(ctx context.Context, cutoff time.Time, batch int) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		"DELETE FROM packets WHERE id IN (SELECT id FROM packets WHERE received_at < ? LIMIT ?)",
		cutoff.UnixMilli(), batch)
	if err != nil {
		return 0, fmt.Errorf("pruning packets: %w", err)
	}
	return res.RowsAffected()
}

// ScanFloodRxSince calls fn, newest first, for each non-trace RX flood packet (the ones with a relay path) since cutoff.
func (r *PacketRepo) ScanFloodRxSince(ctx context.Context, cutoff time.Time, fn func(*PacketRecord)) error {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, received_at, raw, route_type, payload_type, snr, rssi, packet_hash
		FROM packets
		WHERE direction = 'rx' AND route_type IN (?, ?) AND payload_type != ? AND received_at >= ?
		ORDER BY received_at DESC, id DESC`,
		meshcore.RouteTypeFlood, meshcore.RouteTypeTransportFlood, meshcore.PayloadTypeTrace, cutoff.UnixMilli())
	if err != nil {
		return fmt.Errorf("querying packets: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		p := PacketRecord{Direction: "rx"}
		if err := rows.Scan(&p.ID, unixMS(&p.ReceivedAt), &p.Raw, &p.RouteType, &p.PayloadType, &p.SNR, &p.RSSI, &p.PacketHash); err != nil {
			return fmt.Errorf("scanning packet row: %w", err)
		}
		fn(&p)
	}
	return rows.Err()
}

// SentSince maps each packet hash we transmitted since cutoff to whether we started it (an empty path) rather than only relayed it.
func (r *PacketRepo) SentSince(ctx context.Context, cutoff time.Time) (map[string]bool, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT packet_hash, path = '' FROM packets
		WHERE direction = 'tx' AND packet_hash != '' AND received_at >= ?`, cutoff.UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("querying sent packets: %w", err)
	}
	defer rows.Close()
	sent := map[string]bool{}
	for rows.Next() {
		var hash string
		var started bool
		if err := rows.Scan(&hash, &started); err != nil {
			return nil, fmt.Errorf("scanning sent packet: %w", err)
		}
		sent[hash] = sent[hash] || started
	}
	return sent, rows.Err()
}
