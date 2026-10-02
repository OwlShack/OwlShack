-- How long the packet log keeps rows, in days; NULL takes DefaultPacketRetentionDays.

ALTER TABLE settings ADD COLUMN packet_retention_days INTEGER;
