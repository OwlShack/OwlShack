-- When we received each message, by our clock, as a sender's own timestamp can be years out; existing rows take the sent time, the best record they have.
ALTER TABLE messages ADD COLUMN received_at INTEGER NOT NULL DEFAULT 0;
UPDATE messages SET received_at = timestamp;
DROP INDEX IF EXISTS idx_messages_timestamp;
CREATE INDEX idx_messages_received_at ON messages(received_at);
