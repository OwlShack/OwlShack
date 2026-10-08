-- Region scopes: a list and default in settings, an override on each companion, channel, contact, bot and the repeater, and the scope each message carried.

ALTER TABLE settings ADD COLUMN flood_regions TEXT NOT NULL DEFAULT '';
ALTER TABLE settings ADD COLUMN flood_scope TEXT NOT NULL DEFAULT 'everywhere'
	CHECK (flood_scope = 'everywhere' OR flood_scope GLOB 'region:?*');

ALTER TABLE companions ADD COLUMN flood_scope TEXT NOT NULL DEFAULT 'inherit'
	CHECK (flood_scope IN ('inherit', 'everywhere') OR flood_scope GLOB 'region:?*');
ALTER TABLE companion_channels ADD COLUMN flood_scope TEXT NOT NULL DEFAULT 'inherit'
	CHECK (flood_scope IN ('inherit', 'everywhere') OR flood_scope GLOB 'region:?*');
ALTER TABLE companion_contacts ADD COLUMN flood_scope TEXT NOT NULL DEFAULT 'inherit'
	CHECK (flood_scope IN ('inherit', 'everywhere') OR flood_scope GLOB 'region:?*');
ALTER TABLE triggers ADD COLUMN flood_scope TEXT NOT NULL DEFAULT 'inherit'
	CHECK (flood_scope IN ('inherit', 'everywhere') OR flood_scope GLOB 'region:?*');

-- The firmware's default scope is one of its own regions or none; an unset default region sent unscoped.
ALTER TABLE repeater ADD COLUMN flood_scope TEXT NOT NULL DEFAULT 'everywhere'
	CHECK (flood_scope = 'everywhere' OR flood_scope GLOB 'region:?*');
-- "#nz" keys as nz does; a "$" region has no key here, so it already sent unscoped.
UPDATE repeater SET flood_scope = CASE
	WHEN default_region IN ('', '*', '#') OR default_region GLOB '$*' THEN 'everywhere'
	WHEN default_region GLOB '#*' THEN 'region:' || substr(default_region, 2)
	ELSE 'region:' || default_region END;
-- The firmware auto-creates the default region.
UPDATE repeater SET regions = json_insert(regions, '$[#]', json_object('name', default_region, 'parent', '*'))
	WHERE flood_scope != 'everywhere' AND NOT EXISTS (SELECT 1 FROM json_each(repeater.regions)
		WHERE ltrim(json_extract(value, '$.name'), '#') = substr(repeater.flood_scope, 8));
ALTER TABLE repeater DROP COLUMN default_region;

-- What a message was scoped to: 'unknown' is a region not in the list, 'unrecorded' predates this.
ALTER TABLE messages ADD COLUMN flood_scope TEXT NOT NULL DEFAULT 'unrecorded'
	CHECK (flood_scope IN ('unrecorded', 'everywhere', 'unknown') OR flood_scope GLOB 'region:?*');
