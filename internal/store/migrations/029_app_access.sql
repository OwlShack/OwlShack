-- The MeshCore app can connect to a companion over TCP; it is off until turned on, on a port of the companion's own.
ALTER TABLE companions ADD COLUMN app_enabled INTEGER NOT NULL DEFAULT 0 CHECK (app_enabled IN (0, 1));
ALTER TABLE companions ADD COLUMN app_port INTEGER NOT NULL DEFAULT 0;
-- Whether the companion's adverts carry its position; they always did.
ALTER TABLE companions ADD COLUMN share_location INTEGER NOT NULL DEFAULT 1 CHECK (share_location IN (0, 1));

-- Messages the app has yet to collect, held as the firmware's offline queue holds them.
CREATE TABLE app_queue (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	companion_id INTEGER NOT NULL REFERENCES companions(id) ON DELETE CASCADE,
	channel      INTEGER NOT NULL CHECK (channel IN (0, 1)),
	frame        BLOB    NOT NULL
);
CREATE INDEX app_queue_companion ON app_queue (companion_id, id);

-- Turning access off forgets what was waiting, so turning it on again starts from then.
CREATE TRIGGER app_queue_off AFTER UPDATE OF app_enabled ON companions WHEN NEW.app_enabled = 0
BEGIN
	DELETE FROM app_queue WHERE companion_id = NEW.id;
END;

-- A channel keeps its slot, as the firmware's do, so the MeshCore app's per-slot history stays with it; the order they were loaded in is the slots they ran in.
ALTER TABLE companion_channels ADD COLUMN slot INTEGER NOT NULL DEFAULT 0;
UPDATE companion_channels SET slot = (
	SELECT count(*) FROM companion_channels c2
	WHERE c2.companion_id = companion_channels.companion_id AND c2.id < companion_channels.id);
