package store

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// The config tables key on a surrogate INTEGER id, so names, pubkeys and keys are mutable columns nothing references; validation lives in internal/app.

// encodeList/decodeList store a leaf string list in one TEXT column, newline-separated so a value may contain a comma.
func encodeList(items []string) string {
	return strings.Join(items, "\n")
}

func decodeList(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// Settings is the single-row base configuration (radio + process).
type Settings struct {
	LogLevel       *string
	ConnectionType string
	Connection     *string
	BaudRate       *int
	// SPIBoard names the radio hat when Connection is spi://; nil for KISS.
	SPIBoard    *string
	Freq        *float64
	BW          *float64
	SF          *int
	CR          *int
	TX          *int
	ListenAddr  *string
	MapProvider string // basemap: "osm" or "carto"
	// MapDarkStyle recolours OSM in dark mode: "original" or "simplified".
	MapDarkStyle string
	MapTileKey   *string // CARTO basemap API key; nil/"" = keyless tiles
	// ModemToken is the openHop modem's access token; a password, never returned by a config read.
	ModemToken   *string
	PathHashSize *int // default flood path hash width in bytes; nil = 1
	// DutyCyclePct caps TX airtime per hour as a percentage; nil = library default (50%).
	DutyCyclePct *float64
	// PacketRetentionDays is how long the packet log keeps rows, 1-365; the column is NOT NULL and CHECKed.
	PacketRetentionDays int
	SetupComplete       bool
	// FloodRegions is the region list, JSON in settings.flood_regions; FloodScope is the default, "everywhere" or "region:<name>".
	FloodRegions []FloodRegion
	FloodScope   string
}

// FloodRegion is one name in the Settings region list; Parent is the region it sits under, "*" at the top, for organising only.
type FloodRegion struct {
	Name   string `json:"name"`
	Parent string `json:"parent"`
}

type SettingsRepo struct{ db *sql.DB }

func (r *SettingsRepo) Get(ctx context.Context) (*Settings, error) {
	var s Settings
	var floodRegions string
	err := r.db.QueryRowContext(ctx, `
		SELECT log_level, connection_type, connection, baud_rate, spi_board, freq, bw, sf, cr, tx, listen_addr, map_provider, map_dark_style, map_tile_key, path_hash_size, duty_cycle_pct, packet_retention_days, setup_complete, modem_token, flood_regions, flood_scope
		FROM settings WHERE id = 1`).Scan(
		&s.LogLevel, &s.ConnectionType, &s.Connection, &s.BaudRate, &s.SPIBoard,
		&s.Freq, &s.BW, &s.SF, &s.CR, &s.TX, &s.ListenAddr, &s.MapProvider, &s.MapDarkStyle, &s.MapTileKey, &s.PathHashSize,
		&s.DutyCyclePct, &s.PacketRetentionDays, &s.SetupComplete, &s.ModemToken, &floodRegions, &s.FloodScope,
	)
	if err != nil {
		return nil, fmt.Errorf("getting settings: %w", err)
	}
	s.FloodRegions = []FloodRegion{}
	if floodRegions != "" {
		if err := json.Unmarshal([]byte(floodRegions), &s.FloodRegions); err != nil {
			return nil, fmt.Errorf("decoding settings flood regions: %w", err)
		}
	}
	return &s, nil
}

func (r *SettingsRepo) PacketRetentionDays(ctx context.Context) (int, error) {
	var days int
	if err := r.db.QueryRowContext(ctx, "SELECT packet_retention_days FROM settings WHERE id = 1").Scan(&days); err != nil {
		return 0, fmt.Errorf("reading packet retention: %w", err)
	}
	return days, nil
}

func (r *SettingsRepo) Set(ctx context.Context, s *Settings) error {
	regions := s.FloodRegions
	if regions == nil {
		regions = []FloodRegion{}
	}
	floodRegionsJSON, err := json.Marshal(regions)
	if err != nil {
		return fmt.Errorf("encoding settings flood regions: %w", err)
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO settings
			(id, log_level, connection_type, connection, baud_rate, spi_board, freq, bw, sf, cr, tx, listen_addr, map_provider, map_dark_style, map_tile_key, path_hash_size, duty_cycle_pct, packet_retention_days, setup_complete, modem_token, flood_regions, flood_scope)
		VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			log_level=excluded.log_level, connection_type=excluded.connection_type,
			spi_board=excluded.spi_board,
			connection=excluded.connection, baud_rate=excluded.baud_rate, freq=excluded.freq,
			bw=excluded.bw, sf=excluded.sf, cr=excluded.cr, tx=excluded.tx,
			listen_addr=excluded.listen_addr, map_provider=excluded.map_provider, map_dark_style=excluded.map_dark_style, map_tile_key=excluded.map_tile_key,
			path_hash_size=excluded.path_hash_size, duty_cycle_pct=excluded.duty_cycle_pct,
			packet_retention_days=excluded.packet_retention_days,
			setup_complete=excluded.setup_complete, modem_token=excluded.modem_token,
			flood_regions=excluded.flood_regions, flood_scope=excluded.flood_scope`,
		s.LogLevel, s.ConnectionType, s.Connection, s.BaudRate, s.SPIBoard,
		s.Freq, s.BW, s.SF, s.CR, s.TX, s.ListenAddr, s.MapProvider, s.MapDarkStyle, s.MapTileKey, s.PathHashSize,
		s.DutyCyclePct, s.PacketRetentionDays, s.SetupComplete, s.ModemToken, floodRegionsJSON, cmp.Or(s.FloodScope, "everywhere"),
	)
	if err != nil {
		return fmt.Errorf("setting settings: %w", err)
	}
	return nil
}

// MqttSettings is the single-row MQTT config; NodeCompanionID is a surrogate id, so renaming the companion is harmless.
type MqttSettings struct {
	Enabled         *bool
	NodeCompanionID *int64
	IataCode        *string
	StatusInterval  *int
	Owner           *string
	Email           *string
}

type MqttRepo struct{ db *sql.DB }

func (r *MqttRepo) Get(ctx context.Context) (*MqttSettings, error) {
	var m MqttSettings
	err := r.db.QueryRowContext(ctx, `
		SELECT enabled, node_companion_id, iata_code, status_interval, owner, email
		FROM mqtt_settings WHERE id = 1`).Scan(
		&m.Enabled, &m.NodeCompanionID, &m.IataCode, &m.StatusInterval, &m.Owner, &m.Email,
	)
	if err != nil {
		return nil, fmt.Errorf("getting mqtt settings: %w", err)
	}
	return &m, nil
}

func (r *MqttRepo) Set(ctx context.Context, m *MqttSettings) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO mqtt_settings
			(id, enabled, node_companion_id, iata_code, status_interval, owner, email)
		VALUES (1, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			enabled=excluded.enabled, node_companion_id=excluded.node_companion_id,
			iata_code=excluded.iata_code, status_interval=excluded.status_interval,
			owner=excluded.owner, email=excluded.email`,
		m.Enabled, m.NodeCompanionID, m.IataCode, m.StatusInterval, m.Owner, m.Email,
	)
	if err != nil {
		return fmt.Errorf("setting mqtt settings: %w", err)
	}
	return nil
}
