-- The operator's owner for an ambiguous path hash; a NULL pubkey means none of the known peers.

CREATE TABLE IF NOT EXISTS hop_pins (
	hash   TEXT PRIMARY KEY,
	pubkey BLOB
);
