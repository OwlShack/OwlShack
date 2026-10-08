-- MQTT can be fed by the repeater as well as a companion; the repeater is a singleton, so it needs no id.
-- A companion feed that named no node spoke as the first companion, so that choice is written down.

ALTER TABLE mqtt_settings ADD COLUMN node_kind TEXT NOT NULL DEFAULT 'companion'
	CHECK (node_kind IN ('companion', 'repeater'));

UPDATE mqtt_settings SET node_companion_id = (SELECT min(id) FROM companions)
	WHERE node_companion_id IS NULL;
