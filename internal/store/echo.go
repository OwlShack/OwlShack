package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type MessageEcho struct {
	ID           int64
	MessageID    int64
	ReceivedAt   time.Time
	PathHashes   []byte
	PathHashSize int
	Hops         int
	SNR          *float64
	RSSI         *int8
}

type EchoRepo struct {
	db *sql.DB
}

func (r *EchoRepo) Insert(ctx context.Context, e *MessageEcho) error {
	var exists bool
	err := r.db.QueryRowContext(ctx,
		`SELECT EXISTS(
			SELECT 1 FROM message_echoes WHERE message_id = ? AND path_hashes = ?
			UNION ALL
			SELECT 1 FROM messages WHERE id = ? AND path_hashes = ?
		)`,
		e.MessageID, e.PathHashes, e.MessageID, e.PathHashes,
	).Scan(&exists)
	if err != nil {
		return fmt.Errorf("checking existing echo: %w", err)
	}
	if exists {
		return nil
	}

	res, err := r.db.ExecContext(ctx, `
		INSERT INTO message_echoes (message_id, received_at, path_hashes, path_hash_size, hops, snr, rssi)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		e.MessageID, e.ReceivedAt.UnixMilli(), e.PathHashes, e.PathHashSize, e.Hops, e.SNR, e.RSSI,
	)
	if err != nil {
		return fmt.Errorf("inserting echo: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("reading inserted echo id: %w", err)
	}
	e.ID = id
	return nil
}

func (r *EchoRepo) ListByMessage(ctx context.Context, messageID int64) ([]MessageEcho, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, message_id, received_at, path_hashes, path_hash_size, hops, snr, rssi
		FROM message_echoes
		WHERE message_id = ?
		ORDER BY received_at ASC`, messageID)
	if err != nil {
		return nil, fmt.Errorf("querying echoes: %w", err)
	}
	defer rows.Close()

	var echoes []MessageEcho
	for rows.Next() {
		var e MessageEcho
		if err := rows.Scan(
			&e.ID, &e.MessageID, unixMS(&e.ReceivedAt), &e.PathHashes,
			&e.PathHashSize, &e.Hops, &e.SNR, &e.RSSI,
		); err != nil {
			return nil, fmt.Errorf("scanning echo row: %w", err)
		}
		echoes = append(echoes, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating echoes: %w", err)
	}
	return echoes, nil
}

func (r *EchoRepo) CountByMessage(ctx context.Context, messageID int64) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM message_echoes WHERE message_id = ?`, messageID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting echoes: %w", err)
	}
	return count, nil
}
