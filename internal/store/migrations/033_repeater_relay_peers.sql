-- Neighbouring repeaters whose relays shape ours, as JSON [{pubkey, mode}], and how long to wait for a dedupe peer, in packet airtimes.
ALTER TABLE repeater ADD COLUMN relay_peers TEXT NOT NULL DEFAULT '[]';
ALTER TABLE repeater ADD COLUMN dedupe_hold REAL;
