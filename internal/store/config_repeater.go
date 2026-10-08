package store

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// RepeaterRegion is a transport scope the repeater relays, stored as JSON in repeater.regions.
type RepeaterRegion struct {
	Name      string `json:"name"`
	Parent    string `json:"parent,omitempty"`
	DenyFlood bool   `json:"denyFlood,omitempty"`
}

// Repeater is the singleton repeater row (id=1); a missing row means none is configured, since a radio hosts one relay identity.
type Repeater struct {
	Name                string
	PrivateKey          string
	PubKey              string
	Latitude            *float64
	Longitude           *float64
	AdvertInterval      *int
	FloodAdvertInterval *int
	DisableFwd          *bool
	FloodMax            *int
	FloodMaxUnscoped    *int
	FloodMaxAdvert      *int
	LoopDetect          *string
	PathHashSize        *int
	TxDelayFactor       *float64
	DirectTxDelayFactor *float64
	RxDelayBase         *float64
	MultiAcks           *int
	FloodScope          string // "everywhere" or "region:<name>", one of its regions
	HomeRegion          string
	AdminPassword       string
	GuestPassword       string
	OwnerInfo           string
	Regions             []RepeaterRegion
}

type RepeaterRepo struct{ db *sql.DB }

// Get returns the configured repeater, or sql.ErrNoRows when none is set.
func (r *RepeaterRepo) Get(ctx context.Context) (*Repeater, error) {
	var rep Repeater
	var regionsJSON string
	err := r.db.QueryRowContext(ctx, `
		SELECT name, private_key, pubkey, latitude, longitude, advert_interval,
		       flood_advert_interval, disable_fwd, flood_max, flood_max_unscoped, flood_max_advert,
		       loop_detect, path_hash_size, tx_delay_factor, direct_tx_delay_factor, rx_delay_base, multi_acks,
		       flood_scope, home_region, admin_password, guest_password, owner_info, regions
		FROM repeater WHERE id = 1`).Scan(
		&rep.Name, &rep.PrivateKey, &rep.PubKey, &rep.Latitude, &rep.Longitude, &rep.AdvertInterval,
		&rep.FloodAdvertInterval, &rep.DisableFwd, &rep.FloodMax, &rep.FloodMaxUnscoped, &rep.FloodMaxAdvert,
		&rep.LoopDetect, &rep.PathHashSize, &rep.TxDelayFactor, &rep.DirectTxDelayFactor, &rep.RxDelayBase, &rep.MultiAcks, &rep.FloodScope, &rep.HomeRegion, &rep.AdminPassword, &rep.GuestPassword, &rep.OwnerInfo, &regionsJSON)
	if err != nil {
		return nil, err // includes sql.ErrNoRows for "no repeater configured"
	}
	if regionsJSON != "" {
		if err := json.Unmarshal([]byte(regionsJSON), &rep.Regions); err != nil {
			return nil, fmt.Errorf("decoding repeater regions: %w", err)
		}
		for i := range rep.Regions {
			if rep.Regions[i].Parent == "" && rep.Regions[i].Name != "*" {
				rep.Regions[i].Parent = "*" // saved before parents were kept
			}
		}
		dropHashDuplicates(&rep)
	}
	return &rep, nil
}

// dropHashDuplicates keeps the first of "nz" and "#nz", the one the firmware's findByName finds, and points what named the other at it; lists saved before they were one region may hold both.
func dropHashDuplicates(rep *Repeater) {
	key := func(n string) string {
		if n == "*" {
			return n
		}
		return strings.TrimPrefix(n, "#")
	}
	kept := map[string]string{}
	rep.Regions = slices.DeleteFunc(rep.Regions, func(rg RepeaterRegion) bool {
		if _, dup := kept[key(rg.Name)]; dup {
			return true
		}
		kept[key(rg.Name)] = rg.Name
		return false
	})
	for i := range rep.Regions {
		if k, ok := kept[key(rep.Regions[i].Parent)]; ok && rep.Regions[i].Parent != "" {
			rep.Regions[i].Parent = k
		}
	}
	if k, ok := kept[key(rep.HomeRegion)]; ok && rep.HomeRegion != "" {
		rep.HomeRegion = k
	}
	// Merging can close a loop ("nz" under "#nz"), which would refuse the config at startup, so such a region moves to the top.
	parent := map[string]string{}
	for _, rg := range rep.Regions {
		parent[rg.Name] = rg.Parent
	}
	for i, rg := range rep.Regions {
		for p, steps := rg.Parent, 0; p != "" && p != "*" && steps <= len(rep.Regions); p, steps = parent[p], steps+1 {
			if p == rg.Name {
				rep.Regions[i].Parent = "*"
				parent[rg.Name] = "*"
				break
			}
		}
	}
}

// Set upserts the single repeater row (id=1).
func (r *RepeaterRepo) Set(ctx context.Context, rep *Repeater) error {
	regions := rep.Regions
	if regions == nil {
		regions = []RepeaterRegion{}
	}
	regionsJSON, err := json.Marshal(regions)
	if err != nil {
		return fmt.Errorf("encoding repeater regions: %w", err)
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO repeater
			(id, name, private_key, pubkey, latitude, longitude, advert_interval,
			 flood_advert_interval, disable_fwd, flood_max, flood_max_unscoped, flood_max_advert,
			 loop_detect, path_hash_size, tx_delay_factor, direct_tx_delay_factor, rx_delay_base, multi_acks,
			 flood_scope, home_region, admin_password, guest_password, owner_info, regions)
		VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rep.Name, rep.PrivateKey, rep.PubKey, rep.Latitude, rep.Longitude, rep.AdvertInterval,
		rep.FloodAdvertInterval, rep.DisableFwd, rep.FloodMax, rep.FloodMaxUnscoped, rep.FloodMaxAdvert,
		rep.LoopDetect, rep.PathHashSize, rep.TxDelayFactor, rep.DirectTxDelayFactor, rep.RxDelayBase, rep.MultiAcks, cmp.Or(rep.FloodScope, "everywhere"), rep.HomeRegion, rep.AdminPassword, rep.GuestPassword, rep.OwnerInfo, string(regionsJSON))
	if err != nil {
		return fmt.Errorf("upserting repeater: %w", err)
	}
	return nil
}

// Clear removes the repeater and the channel map it published, which the next repeater created would otherwise inherit.
func (r *RepeaterRepo) Clear(ctx context.Context) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("clearing repeater: %w", err)
	}
	defer tx.Rollback()
	if err := deleteTelemetryMapForNode(ctx, tx, TelemetryNode{Kind: NodeKindRepeater, ID: RepeaterNodeID}); err != nil {
		return fmt.Errorf("clearing the repeater's telemetry map: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM repeater`); err != nil {
		return fmt.Errorf("clearing repeater: %w", err)
	}
	return tx.Commit()
}
