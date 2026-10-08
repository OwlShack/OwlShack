package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const DefaultMaxMessages = 5000

// messageColumns is the SELECT list for a full Message row; keep in sync with scanMessage and Insert.
const messageColumns = `id, companion_id, channel, channel_hash, sender, text, direction, timestamp, received_at, snr, rssi, confirmed, path_hashes, path_hash_size, hops, status, flood_scope`

type Message struct {
	ID          int64
	CompanionID int64
	Channel     string
	ChannelHash byte
	Sender      string
	Text        string
	Direction   string
	// Timestamp is when the sender says it wrote the message, by its clock; ReceivedAt is ours.
	Timestamp    time.Time
	ReceivedAt   time.Time
	SNR          *float64
	RSSI         *int8
	RepeatCount  *int
	PathHashes   []byte
	PathHashSize *int
	Hops         *int
	Status       *string
	// FloodScope is what the flood carried: "everywhere", "region:<name>", "unknown" (not in the list) or "unrecorded".
	FloodScope string
}

type MessageRepo struct {
	db      *sql.DB
	maxRows int
}

// scanMessage scans one full Message row from a *sql.Row or *sql.Rows.
func scanMessage(s interface{ Scan(...any) error }) (Message, error) {
	var m Message
	err := s.Scan(
		&m.ID, &m.CompanionID, &m.Channel, &m.ChannelHash,
		&m.Sender, &m.Text, &m.Direction, unixMS(&m.Timestamp), unixMS(&m.ReceivedAt),
		&m.SNR, &m.RSSI, &m.RepeatCount,
		&m.PathHashes, &m.PathHashSize, &m.Hops, &m.Status, &m.FloodScope,
	)
	return m, err
}

func (r *MessageRepo) Insert(ctx context.Context, m *Message) error {
	if m.ReceivedAt.IsZero() {
		return errors.New("inserting message: no receive time")
	}
	if m.FloodScope == "" {
		return errors.New("inserting message: no flood scope") // "unrecorded" is only for rows from before scopes
	}
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO messages (companion_id, channel, channel_hash, sender, text, direction, timestamp, received_at, snr, rssi, confirmed, path_hashes, path_hash_size, hops, status, flood_scope)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.CompanionID, m.Channel, m.ChannelHash, m.Sender, m.Text, m.Direction, m.Timestamp.UnixMilli(), m.ReceivedAt.UnixMilli(), m.SNR, m.RSSI, m.RepeatCount,
		m.PathHashes, m.PathHashSize, m.Hops, m.Status, m.FloodScope,
	)
	if err != nil {
		return fmt.Errorf("inserting message: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("reading inserted id: %w", err)
	}
	m.ID = id
	return r.prune(ctx)
}

func (r *MessageRepo) UpdateStatus(ctx context.Context, id int64, status string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE messages SET status = ? WHERE id = ?`, status, id)
	if err != nil {
		return fmt.Errorf("updating message status: %w", err)
	}
	return nil
}

func (r *MessageRepo) GetByID(ctx context.Context, id int64) (*Message, error) {
	m, err := scanMessage(r.db.QueryRowContext(ctx,
		"SELECT "+messageColumns+" FROM messages WHERE id = ?", id))
	if err != nil {
		return nil, fmt.Errorf("getting message by id: %w", err)
	}
	return &m, nil
}

func (r *MessageRepo) IncrementRepeatCount(ctx context.Context, id int64) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `
		UPDATE messages SET confirmed = COALESCE(confirmed, 0) + 1 WHERE id = ?
		RETURNING confirmed`, id).Scan(&count)
	if err != nil {
		return count, fmt.Errorf("incrementing repeat count: %w", err)
	}
	return count, nil
}

func (r *MessageRepo) List(ctx context.Context, companionID int64, channel string, limit, offset int) ([]Message, error) {
	if limit <= 0 {
		limit = 100
	}

	query := "SELECT " + messageColumns + `
		FROM messages
		WHERE companion_id = ? AND channel = ?
		ORDER BY id DESC
		LIMIT ? OFFSET ?`

	rows, err := r.db.QueryContext(ctx, query, companionID, channel, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("querying messages: %w", err)
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning message row: %w", err)
		}
		messages = append(messages, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating messages: %w", err)
	}
	return messages, nil
}

// ListBefore returns up to limit messages with id < beforeID, newest-first; cursor-based so concurrent inserts can't shift the window.
func (r *MessageRepo) ListBefore(ctx context.Context, companionID int64, channel string, beforeID int64, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 100
	}

	rows, err := r.db.QueryContext(ctx, "SELECT "+messageColumns+`
		FROM messages
		WHERE companion_id = ? AND channel = ? AND id < ?
		ORDER BY id DESC
		LIMIT ?`, companionID, channel, beforeID, limit)
	if err != nil {
		return nil, fmt.Errorf("querying messages before id: %w", err)
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning message row: %w", err)
		}
		messages = append(messages, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating messages: %w", err)
	}
	return messages, nil
}

func (r *MessageRepo) ListAfter(ctx context.Context, companionID int64, channel string, afterID int64, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 200
	}

	rows, err := r.db.QueryContext(ctx, "SELECT "+messageColumns+`
		FROM messages
		WHERE companion_id = ? AND channel = ? AND id > ?
		ORDER BY id ASC
		LIMIT ?`, companionID, channel, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("querying messages after id: %w", err)
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning message row: %w", err)
		}
		messages = append(messages, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating messages: %w", err)
	}
	return messages, nil
}

func (r *MessageRepo) ListAll(ctx context.Context, companionID int64, limit, offset int) ([]Message, error) {
	if limit <= 0 {
		limit = 100
	}

	rows, err := r.db.QueryContext(ctx, "SELECT "+messageColumns+`
		FROM messages
		WHERE companion_id = ?
		ORDER BY id DESC
		LIMIT ? OFFSET ?`, companionID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("querying all messages: %w", err)
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning message row: %w", err)
		}
		messages = append(messages, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating messages: %w", err)
	}
	return messages, nil
}

// LatestRx returns the most recently inserted rx message for a channel, or nil.
func (r *MessageRepo) LatestRx(ctx context.Context, companionID int64, channel string) (*Message, error) {
	m, err := scanMessage(r.db.QueryRowContext(ctx, "SELECT "+messageColumns+`
		FROM messages
		WHERE companion_id = ? AND channel = ? AND direction = 'rx'
		ORDER BY id DESC LIMIT 1`, companionID, channel))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting latest rx message: %w", err)
	}
	return &m, nil
}

func (r *MessageRepo) Delete(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM messages WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("deleting message: %w", err)
	}
	return nil
}

func (r *MessageRepo) DeleteByChannel(ctx context.Context, companionID int64, channel string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM messages WHERE companion_id = ? AND channel = ?`, companionID, channel)
	if err != nil {
		return fmt.Errorf("deleting messages by channel: %w", err)
	}
	return nil
}

func (r *MessageRepo) DistinctSenders(ctx context.Context, companionID int64, channel string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT DISTINCT sender FROM messages
		WHERE companion_id = ? AND channel = ? AND direction = 'rx' AND sender != ''
		ORDER BY sender`, companionID, channel)
	if err != nil {
		return nil, fmt.Errorf("querying distinct senders: %w", err)
	}
	defer rows.Close()

	var senders []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("scanning sender row: %w", err)
		}
		senders = append(senders, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating senders: %w", err)
	}
	return senders, nil
}

func (r *MessageRepo) prune(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx, `
		DELETE FROM messages WHERE id IN (
			SELECT id FROM messages ORDER BY id ASC LIMIT MAX(0,
				(SELECT COUNT(*) FROM messages) - ?
			)
		)`, r.maxRows)
	if err != nil {
		return fmt.Errorf("pruning messages: %w", err)
	}
	return nil
}
