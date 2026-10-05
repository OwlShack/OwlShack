-- How long the packet log keeps rows, in days; always set, and the CHECK refuses a bad value whatever writes it.

ALTER TABLE settings ADD COLUMN packet_retention_days INTEGER NOT NULL DEFAULT 7
	CHECK (packet_retention_days BETWEEN 1 AND 365);
