-- With no "*" region a repeater now relays unscoped flood, as the firmware's wildcard does, so one that had none keeps denying it.
UPDATE repeater SET regions = json_insert(regions, '$[#]', json('{"name":"*","denyFlood":true}'))
	WHERE NOT EXISTS (SELECT 1 FROM json_each(repeater.regions) WHERE json_extract(value, '$.name') = '*');
