package store

import (
	"context"
	"database/sql"
	"fmt"
)

// Companion is a node personality the bot runs; id is the stable key, name/pubkey/private_key are all mutable.
type Companion struct {
	ID             int64
	Name           string
	PrivateKey     string
	PubKey         string
	Latitude       *float64
	Longitude      *float64
	AdvertInterval *int
	PathHashSize   *int // nil = inherit the global settings default
	// DMPolicy is who may DM this companion: "contacts", "allowlist" or "anyone".
	DMPolicy string
	// DMAllow is the "allowlist" policy's pubkeys, newline-encoded in the column.
	DMAllow []string
	// Telem* is who may read each telemetry class: "deny", "selected" or "contacts".
	TelemBase string
	TelemLoc  string
	TelemEnv  string
	// FloodScope is "inherit", "everywhere" or "region:<name>".
	FloodScope string
	// AppEnabled lets the MeshCore app connect on AppPort; the port is kept while access is off.
	AppEnabled bool
	AppPort    int
	// ShareLocation puts the position in the companion's adverts.
	ShareLocation bool
}

type CompanionRepo struct{ db *sql.DB }

func scanCompanion(s interface{ Scan(...any) error }) (*Companion, error) {
	var c Companion
	var dmAllow string
	if err := s.Scan(&c.ID, &c.Name, &c.PrivateKey, &c.PubKey, &c.Latitude, &c.Longitude, &c.AdvertInterval, &c.PathHashSize, &c.DMPolicy, &dmAllow,
		&c.TelemBase, &c.TelemLoc, &c.TelemEnv, &c.FloodScope, &c.AppEnabled, &c.AppPort, &c.ShareLocation); err != nil {
		return nil, err
	}
	c.DMAllow = decodeList(dmAllow)
	return &c, nil
}

const companionCols = `id, name, private_key, pubkey, latitude, longitude, advert_interval, path_hash_size, dm_policy, dm_allow,
	telem_base, telem_loc, telem_env, flood_scope, app_enabled, app_port, share_location`

// DMPolicyContacts is the pre-column behaviour and the value written for an unset policy.
const DMPolicyContacts = "contacts"

// TelemDeny is the firmware default and what an unset telemetry mode is written as.
const TelemDeny = "deny"

func dmPolicyOrDefault(p string) string {
	if p == "" {
		return DMPolicyContacts
	}
	return p
}

// scopeOrInherit is what an unset level is written as; the API refuses an unset one before it gets here.
func scopeOrInherit(s string) string {
	if s == "" {
		return "inherit"
	}
	return s
}

func telemModeOrDefault(m string) string {
	if m == "" {
		return TelemDeny
	}
	return m
}

func (r *CompanionRepo) List(ctx context.Context) ([]Companion, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+companionCols+`
		FROM companions ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("querying companions: %w", err)
	}
	defer rows.Close()
	var out []Companion
	for rows.Next() {
		c, err := scanCompanion(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning companion row: %w", err)
		}
		out = append(out, *c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating companions: %w", err)
	}
	return out, nil
}

func (r *CompanionRepo) Get(ctx context.Context, id int64) (*Companion, error) {
	c, err := scanCompanion(r.db.QueryRowContext(ctx, `
		SELECT `+companionCols+`
		FROM companions WHERE id = ?`, id))
	if err != nil {
		return nil, fmt.Errorf("getting companion: %w", err)
	}
	return c, nil
}

// IDByName resolves the surrogate id from the current name, returning sql.ErrNoRows when no companion has it.
func (r *CompanionRepo) IDByName(ctx context.Context, name string) (int64, error) {
	var id int64
	err := r.db.QueryRowContext(ctx, `SELECT id FROM companions WHERE name = ?`, name).Scan(&id)
	if err != nil {
		return id, fmt.Errorf("resolving companion id by name: %w", err)
	}
	return id, nil
}

// Create inserts a companion and sets c.ID to the new surrogate key.
func (r *CompanionRepo) Create(ctx context.Context, c *Companion) error {
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO companions (name, private_key, pubkey, latitude, longitude, advert_interval, path_hash_size, dm_policy, dm_allow,
			telem_base, telem_loc, telem_env, flood_scope, app_enabled, app_port, share_location)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.Name, c.PrivateKey, c.PubKey, c.Latitude, c.Longitude, c.AdvertInterval, c.PathHashSize, dmPolicyOrDefault(c.DMPolicy), encodeList(c.DMAllow),
		telemModeOrDefault(c.TelemBase), telemModeOrDefault(c.TelemLoc), telemModeOrDefault(c.TelemEnv), scopeOrInherit(c.FloodScope), c.AppEnabled, c.AppPort, c.ShareLocation)
	if err != nil {
		return fmt.Errorf("inserting companion: %w", err)
	}
	c.ID, err = res.LastInsertId()
	if err != nil {
		return fmt.Errorf("reading inserted companion id: %w", err)
	}
	return nil
}

func (r *CompanionRepo) Update(ctx context.Context, c *Companion) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE companions SET name=?, private_key=?, pubkey=?, latitude=?, longitude=?, advert_interval=?, path_hash_size=?, dm_policy=?, dm_allow=?,
			telem_base=?, telem_loc=?, telem_env=?, flood_scope=?, app_enabled=?, app_port=?, share_location=?
		WHERE id=?`,
		c.Name, c.PrivateKey, c.PubKey, c.Latitude, c.Longitude, c.AdvertInterval, c.PathHashSize, dmPolicyOrDefault(c.DMPolicy), encodeList(c.DMAllow),
		telemModeOrDefault(c.TelemBase), telemModeOrDefault(c.TelemLoc), telemModeOrDefault(c.TelemEnv), scopeOrInherit(c.FloodScope), c.AppEnabled, c.AppPort, c.ShareLocation, c.ID)
	if err != nil {
		return fmt.Errorf("updating companion: %w", err)
	}
	return nil
}

func (r *CompanionRepo) Delete(ctx context.Context, id int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("deleting companion: %w", err)
	}
	defer tx.Rollback()
	// Cleared here rather than by a cascade, because the map keys on node kind and id and a row may be the repeater's.
	if err := deleteTelemetryMapForNode(ctx, tx, TelemetryNode{Kind: NodeKindCompanion, ID: id}); err != nil {
		return fmt.Errorf("deleting the companion's telemetry map: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM companions WHERE id = ?`, id); err != nil {
		return fmt.Errorf("deleting companion: %w", err)
	}
	return tx.Commit()
}

// CompanionChannel is a companion-owned channel; an empty private_key means a public/hashtag-derived one.
type CompanionChannel struct {
	ID          int64
	CompanionID int64
	Name        string
	PrivateKey  string
	FloodScope  string // "inherit", "everywhere" or "region:<name>"
	// Slot is the node's channel slot; nil on Create takes the companion's first free one.
	Slot *int
}

type ChannelRepo struct{ db *sql.DB }

const channelCols = `id, companion_id, name, private_key, flood_scope, slot`

func scanChannel(s interface{ Scan(...any) error }) (CompanionChannel, error) {
	var c CompanionChannel
	var slot int
	err := s.Scan(&c.ID, &c.CompanionID, &c.Name, &c.PrivateKey, &c.FloodScope, &slot)
	c.Slot = &slot
	return c, err
}

// List returns every channel across all companions.
func (r *ChannelRepo) List(ctx context.Context) ([]CompanionChannel, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+channelCols+`
		FROM companion_channels ORDER BY companion_id ASC, slot ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("querying channels: %w", err)
	}
	defer rows.Close()
	var out []CompanionChannel
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning channel row: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating channels: %w", err)
	}
	return out, nil
}

func (r *ChannelRepo) ListByCompanion(ctx context.Context, companionID int64) ([]CompanionChannel, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+channelCols+`
		FROM companion_channels WHERE companion_id = ? ORDER BY slot ASC, id ASC`, companionID)
	if err != nil {
		return nil, fmt.Errorf("querying channels by companion: %w", err)
	}
	defer rows.Close()
	var out []CompanionChannel
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning channel row: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating channels: %w", err)
	}
	return out, nil
}

func (r *ChannelRepo) Get(ctx context.Context, id int64) (*CompanionChannel, error) {
	c, err := scanChannel(r.db.QueryRowContext(ctx, `
		SELECT `+channelCols+` FROM companion_channels WHERE id = ?`, id))
	if err != nil {
		return nil, fmt.Errorf("getting channel: %w", err)
	}
	return &c, nil
}

func (r *ChannelRepo) Create(ctx context.Context, c *CompanionChannel) error {
	slot := -1
	if c.Slot != nil {
		slot = *c.Slot
	}
	// -1 asks for the lowest slot no other channel of the companion holds.
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO companion_channels (companion_id, name, private_key, flood_scope, slot)
		VALUES (?1, ?2, ?3, ?4, CASE WHEN ?5 >= 0 THEN ?5 ELSE (
			SELECT min(s.n) FROM (SELECT 0 AS n UNION ALL SELECT slot + 1 FROM companion_channels WHERE companion_id = ?1) s
			WHERE s.n NOT IN (SELECT slot FROM companion_channels WHERE companion_id = ?1)) END)`,
		c.CompanionID, c.Name, c.PrivateKey, scopeOrInherit(c.FloodScope), slot)
	if err != nil {
		return fmt.Errorf("inserting channel: %w", err)
	}
	c.ID, err = res.LastInsertId()
	if err != nil {
		return fmt.Errorf("reading inserted channel id: %w", err)
	}
	return nil
}

func (r *ChannelRepo) Update(ctx context.Context, c *CompanionChannel) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE companion_channels SET name=?, private_key=?, flood_scope=? WHERE id=?`,
		c.Name, c.PrivateKey, scopeOrInherit(c.FloodScope), c.ID)
	if err != nil {
		return fmt.Errorf("updating channel: %w", err)
	}
	return nil
}

func (r *ChannelRepo) Delete(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM companion_channels WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("deleting channel: %w", err)
	}
	return nil
}
