package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type Conversation struct {
	ID          string
	Type        string // "channel" or "contact"
	Name        string
	LastMessage *ConversationMessage
	UnreadCount int
	LastActive  time.Time
	// LastMessageID orders threads: a remote node's clock can be wrong, so LastActive is not a sort key.
	LastMessageID int64
}

type ConversationMessage struct {
	Text      string
	Sender    string
	Direction string
	Timestamp time.Time
}

type ConversationRepo struct {
	db *sql.DB
}

type ContactInfo struct {
	PubKeyHex string
	Name      string
}

func (r *ConversationRepo) List(ctx context.Context, companionID int64, channelNames []string, contacts []ContactInfo) ([]Conversation, error) {
	convos := make([]Conversation, 0)

	for _, ch := range channelNames {
		conv, err := r.channelConversation(ctx, companionID, ch)
		if err != nil {
			return nil, err
		}
		convos = append(convos, *conv)
	}

	for _, ct := range contacts {
		conv, err := r.contactConversation(ctx, companionID, ct)
		if err != nil {
			return nil, err
		}
		convos = append(convos, *conv)
	}

	return convos, nil
}

func (r *ConversationRepo) channelConversation(ctx context.Context, companionID int64, channel string) (*Conversation, error) {
	conv := &Conversation{
		ID:   "channel:" + channel,
		Type: "channel",
		Name: channel,
	}

	var lastID sql.NullInt64
	var text, sender, direction sql.NullString
	var ts, received time.Time
	err := r.db.QueryRowContext(ctx, `
		SELECT id, text, sender, direction, timestamp, received_at
		FROM messages
		WHERE companion_id = ? AND channel = ?
		ORDER BY id DESC LIMIT 1`,
		companionID, channel,
	).Scan(&lastID, &text, &sender, &direction, unixMS(&ts), unixMS(&received))

	if err == nil && text.Valid {
		conv.LastMessage = &ConversationMessage{
			Text:      text.String,
			Sender:    sender.String,
			Direction: direction.String,
			Timestamp: ts,
		}
		conv.LastActive = received
		conv.LastMessageID = lastID.Int64
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("querying last channel message: %w", err)
	}

	var lastReadID int64
	err = r.db.QueryRowContext(ctx, `
		SELECT last_read_id FROM conversation_reads
		WHERE companion_id = ? AND conversation_id = ?`,
		companionID, "channel:"+channel,
	).Scan(&lastReadID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("reading channel last_read_id: %w", err)
	}

	var unread int
	err = r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM messages
		WHERE companion_id = ? AND channel = ? AND id > ? AND direction = 'rx'`,
		companionID, channel, lastReadID,
	).Scan(&unread)
	if err != nil {
		return nil, fmt.Errorf("counting unread channel messages: %w", err)
	}
	conv.UnreadCount = unread

	return conv, nil
}

func (r *ConversationRepo) contactConversation(ctx context.Context, companionID int64, ct ContactInfo) (*Conversation, error) {
	channelKey := "dm:" + ct.PubKeyHex
	conv := &Conversation{
		ID:   "contact:" + ct.PubKeyHex,
		Type: "contact",
		Name: ct.Name,
	}

	var lastID sql.NullInt64
	var text, sender, direction sql.NullString
	var ts, received time.Time
	err := r.db.QueryRowContext(ctx, `
		SELECT id, text, sender, direction, timestamp, received_at
		FROM messages
		WHERE companion_id = ? AND channel = ?
		ORDER BY id DESC LIMIT 1`,
		companionID, channelKey,
	).Scan(&lastID, &text, &sender, &direction, unixMS(&ts), unixMS(&received))

	if err == nil && text.Valid {
		conv.LastMessage = &ConversationMessage{
			Text:      text.String,
			Sender:    sender.String,
			Direction: direction.String,
			Timestamp: ts,
		}
		conv.LastActive = received
		conv.LastMessageID = lastID.Int64
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("querying last contact message: %w", err)
	}

	var lastReadID int64
	err = r.db.QueryRowContext(ctx, `
		SELECT last_read_id FROM conversation_reads
		WHERE companion_id = ? AND conversation_id = ?`,
		companionID, "contact:"+ct.PubKeyHex,
	).Scan(&lastReadID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("reading contact last_read_id: %w", err)
	}

	var unread int
	err = r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM messages
		WHERE companion_id = ? AND channel = ? AND id > ? AND direction = 'rx'`,
		companionID, channelKey, lastReadID,
	).Scan(&unread)
	if err != nil {
		return nil, fmt.Errorf("counting unread contact messages: %w", err)
	}
	conv.UnreadCount = unread

	return conv, nil
}

func (r *ConversationRepo) MarkRead(ctx context.Context, companionID int64, conversationID string, lastReadID int64) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO conversation_reads (companion_id, conversation_id, last_read_id)
		VALUES (?, ?, ?)
		ON CONFLICT (companion_id, conversation_id)
		DO UPDATE SET last_read_id = MAX(excluded.last_read_id, conversation_reads.last_read_id)`,
		companionID, conversationID, lastReadID,
	)
	if err != nil {
		return fmt.Errorf("marking conversation read: %w", err)
	}
	return nil
}
