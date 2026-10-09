package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/OwlShack/meshcore-go/node"

	"github.com/OwlShack/OwlShack/internal/api"
	"github.com/OwlShack/OwlShack/internal/config"
	"github.com/OwlShack/OwlShack/internal/store"
)

// Every per-resource write validates the assembled result before it touches the DB, and keeps ids across renames.

// configMutate is the shared write transaction, serialized on the store's writer goroutine.
func (b *backend) configMutate(ctx context.Context, apply func(*configRows) error, persist func(*store.Store) error) error {
	return writeConfigTx(ctx, b.db, b.reload, func(rows *configRows) error {
		wasNodeless := mqttNodeless(rows)
		if err := apply(rows); err != nil {
			return err
		}
		if verr := assembleFromRows(rows).Validate(); verr != nil {
			return verr
		}
		if !wasNodeless && mqttNodeless(rows) {
			return api.Invalid(errors.New("MQTT would publish with no node to speak as; pick its node on the MQTT page first"))
		}
		return persist(b.db)
	})
}

// writeConfigTx runs load → validate → persist inside the single writer, and reloads only if work succeeded.
func writeConfigTx(ctx context.Context, db *store.Store, reload func() error, work func(*configRows) error) error {
	var resultErr error
	db.WriteSync(func() {
		rows, err := loadConfigRows(ctx, db)
		if err != nil {
			resultErr = err
			return
		}
		resultErr = work(rows)
	})
	if resultErr != nil {
		return resultErr
	}
	if reload != nil {
		return reload()
	}
	return nil
}

func filterOut[T any](s []T, drop func(T) bool) []T {
	out := make([]T, 0, len(s))
	for _, x := range s {
		if !drop(x) {
			out = append(out, x)
		}
	}
	return out
}

// or returns p when set, else the default d.
func or[T any](p, d *T) *T {
	if p != nil {
		return p
	}
	return d
}

func (b *backend) SaveSettings(ctx context.Context, in api.SettingsInput) error {
	if in.PacketRetentionDays == nil {
		return errors.New("packetRetentionDays is required")
	}
	def := config.DefaultConfig()
	var row store.Settings
	return b.configMutate(ctx,
		func(rows *configRows) error {
			// Derived, never taken from the caller: the connection string is what Setup switches
			// on, so a stored backend that disagreed with it would be a lie the UI reads back.
			ct := "kiss"
			conn := or(in.Connection, def.Connection)
			if scheme, _, ok := config.ParseConnection(strDeref(conn)); ok && scheme != "serial" && scheme != "tcp" {
				ct = scheme
			}
			// SetupComplete changes only when explicitly provided, so a radio edit never re-opens the wizard.
			setup := rows.settings.SetupComplete
			if in.SetupComplete != nil {
				setup = *in.SetupComplete
			}
			prevKey := rows.settings.MapTileKey
			prevToken := rows.settings.ModemToken
			prevBoard := rows.settings.SPIBoard
			row = store.Settings{
				LogLevel:            in.LogLevel,
				ConnectionType:      ct,
				Connection:          conn,
				BaudRate:            or(in.BaudRate, def.BaudRate),
				SPIBoard:            or(in.SPIBoard, prevBoard),
				Freq:                or(in.Freq, def.Freq),
				BW:                  or(in.BW, def.Bw),
				SF:                  or(in.SF, u8ToIntPtr(def.SF)),
				CR:                  or(in.CR, u8ToIntPtr(def.CR)),
				TX:                  or(in.TX, u8ToIntPtr(def.TX)),
				ListenAddr:          in.ListenAddr,
				MapProvider:         *or(in.MapProvider, &rows.settings.MapProvider),
				MapDarkStyle:        *or(in.MapDarkStyle, &rows.settings.MapDarkStyle),
				MapTileKey:          or(in.MapTileKey, prevKey),
				ModemToken:          or(in.ModemToken, prevToken),
				PathHashSize:        in.PathHashSize,
				DutyCyclePct:        in.DutyCycle,
				PacketRetentionDays: *in.PacketRetentionDays,
				SetupComplete:       setup,
				FloodRegions:        rows.settings.FloodRegions, // their own endpoint
				FloodScope:          rows.settings.FloodScope,
			}
			rows.settings = &row
			return nil
		},
		func(st *store.Store) error { return st.Settings.Set(ctx, &row) },
	)
}

// missingRow is a 404 for an update naming an id that isn't stored; 0 is a create.
func missingRow[T any](rows []T, id int64, idOf func(T) int64, what string) error {
	if id == 0 || slices.ContainsFunc(rows, func(r T) bool { return idOf(r) == id }) {
		return nil
	}
	return api.Failed(http.StatusNotFound, fmt.Errorf("no %s with id %d", what, id))
}

// listedScope refuses a save that moves to a region missing from the Settings list; one left from before the list was enforced stays until changed, and loading never checks.
func listedScope(scope, prev string, regions []store.FloodRegion) error {
	name, ok := config.FloodScope(scope).RegionName()
	if !ok || scope == prev || slices.ContainsFunc(regions, func(r store.FloodRegion) bool { return config.SameRegionName(r.Name, name) }) {
		return nil
	}
	return api.Invalid(fmt.Errorf("region %q is not in the region list in Settings; add it there first", name))
}

// regionsStillUsed refuses dropping a region from the Settings list while the default, a companion, channel, bot or contact sends in it, naming them.
func (b *backend) regionsStillUsed(ctx context.Context, rows *configRows, keep []store.FloodRegion, newDefault string) error {
	listed := func(list []store.FloodRegion, name string) bool {
		return slices.ContainsFunc(list, func(r store.FloodRegion) bool { return config.SameRegionName(r.Name, name) })
	}
	var users []string
	use := func(scope, what string) {
		if name, ok := config.FloodScope(scope).RegionName(); ok && listed(rows.settings.FloodRegions, name) && !listed(keep, name) {
			users = append(users, fmt.Sprintf("%s (%s)", what, name))
		}
	}
	use(newDefault, "the default region")
	compName := make(map[int64]string, len(rows.companions))
	for _, c := range rows.companions {
		compName[c.ID] = c.Name
		use(c.FloodScope, "companion "+c.Name)
	}
	for _, ch := range rows.channels {
		use(ch.FloodScope, "channel "+ch.Name+" on "+compName[ch.CompanionID])
	}
	for _, t := range rows.triggers {
		use(t.FloodScope, fmt.Sprintf("%s bot %d on %s", t.Type, t.ID, compName[t.CompanionID]))
	}
	contacts, err := b.db.Contacts.RegionScoped(ctx)
	if err != nil {
		return err
	}
	for _, c := range contacts {
		use(c.FloodScope, "contact "+c.Name+" on "+compName[c.CompanionID])
	}
	if len(users) > 0 {
		return api.Invalid(fmt.Errorf("a region still in use can't be removed; first change %s", strings.Join(users, ", ")))
	}
	return nil
}

// SaveFloodRegions replaces the region list and the default together, as the one form that edits them.
func (b *backend) SaveFloodRegions(ctx context.Context, in api.FloodRegionsInput) error {
	if in.Regions == nil {
		return errors.New("regions is required")
	}
	if err := config.RequireScope(in.FloodScope, false); err != nil {
		return err
	}
	var row store.Settings
	return b.configMutate(ctx,
		func(rows *configRows) error {
			if err := listedScope(in.FloodScope, rows.settings.FloodScope, *in.Regions); err != nil {
				return err
			}
			if err := b.regionsStillUsed(ctx, rows, *in.Regions, in.FloodScope); err != nil {
				return err
			}
			row = *rows.settings
			row.FloodRegions = *in.Regions
			row.FloodScope = in.FloodScope
			rows.settings = &row
			return nil
		},
		func(st *store.Store) error { return st.Settings.Set(ctx, &row) },
	)
}

func (b *backend) SetContactFloodScope(ctx context.Context, companionID int64, pubkey []byte, scope string) error {
	if err := config.RequireScope(scope, true); err != nil {
		return err
	}
	var err error
	b.db.WriteSync(func() {
		var s *store.Settings
		if s, err = b.db.Settings.Get(ctx); err != nil {
			return
		}
		prev := ""
		if c, gerr := b.db.Contacts.Get(ctx, companionID, pubkey); gerr == nil {
			prev = c.FloodScope
		}
		if err = listedScope(scope, prev, s.FloodRegions); err != nil {
			return
		}
		err = b.db.Contacts.SetFloodScope(ctx, companionID, pubkey, scope)
	})
	return err
}

func (b *backend) SaveMqtt(ctx context.Context, in api.MqttInput) error {
	if in.NodeKind != config.MqttNodeCompanion && in.NodeKind != config.MqttNodeRepeater {
		return api.Invalid(fmt.Errorf(`nodeKind is required: %q or %q`, config.MqttNodeCompanion, config.MqttNodeRepeater))
	}
	if in.NodeKind == config.MqttNodeRepeater && in.NodeCompanionID != nil {
		return api.Invalid(errors.New("nodeCompanionId names a companion; leave it out when the repeater feeds MQTT"))
	}
	row := store.MqttSettings{
		Enabled:         in.Enabled,
		NodeKind:        in.NodeKind,
		NodeCompanionID: in.NodeCompanionID,
		IataCode:        in.IataCode,
		StatusInterval:  in.StatusInterval,
		Owner:           in.Owner,
		Email:           in.Email,
	}
	return b.configMutate(ctx,
		func(rows *configRows) error {
			if in.NodeKind == config.MqttNodeRepeater && rows.repeater == nil {
				return api.Invalid(errors.New("no repeater is configured to feed MQTT"))
			}
			if id := in.NodeCompanionID; id != nil && !slices.ContainsFunc(rows.companions, func(c store.Companion) bool { return c.ID == *id }) {
				return api.Invalid(fmt.Errorf("no companion with id %d to feed MQTT", *id))
			}
			if in.NodeKind == config.MqttNodeCompanion && in.NodeCompanionID == nil && len(rows.companions) > 0 {
				return api.Invalid(errors.New("nodeCompanionId is required: pick the companion that feeds MQTT"))
			}
			rows.mqtt = &row
			if in.NodeKind == config.MqttNodeCompanion && len(rows.companions) == 0 && mqttFeeding(rows) {
				return api.Invalid(errors.New("there is no companion to feed MQTT; pick the repeater or add a companion"))
			}
			return nil
		},
		func(st *store.Store) error { return st.Mqtt.Set(ctx, &row) },
	)
}

// mqttFeeding is whether MQTT is on, so its node is in use: enabled, with an enabled broker to publish to.
func mqttFeeding(rows *configRows) bool {
	return (rows.mqtt.Enabled == nil || *rows.mqtt.Enabled) && slices.ContainsFunc(rows.brokers, func(b store.Broker) bool { return b.Enabled })
}

// mqttNodeless is MQTT publishing while the node it speaks as is missing, so it would send nothing while looking on.
func mqttNodeless(rows *configRows) bool {
	if !mqttFeeding(rows) {
		return false
	}
	if rows.mqtt.NodeKind == config.MqttNodeRepeater {
		return rows.repeater == nil
	}
	id := rows.mqtt.NodeCompanionID
	return id == nil || !slices.ContainsFunc(rows.companions, func(c store.Companion) bool { return c.ID == *id })
}

// errFeedsMqtt refuses deleting the node MQTT speaks as, so the feed never changes identity unasked.
func errFeedsMqtt(what string) error {
	return api.Failed(http.StatusConflict, fmt.Errorf("%s feeds MQTT; pick another node on the MQTT page, or turn MQTT off, first", what))
}

func (b *backend) SaveBroker(ctx context.Context, in api.BrokerInput) (int64, error) {
	var row store.Broker
	err := b.configMutate(ctx,
		func(rows *configRows) error {
			if err := missingRow(rows.brokers, in.ID, func(x store.Broker) int64 { return x.ID }, "broker"); err != nil {
				return err
			}
			row = store.Broker{
				ID: in.ID, Name: in.Name, Enabled: in.Enabled, Dedup: in.Dedup,
				Transport: in.Transport, Host: in.Host, Port: in.Port,
				PacketTopic: in.PacketTopic, StatusTopic: in.StatusTopic,
				DisallowedPacketTypes: in.DisallowedPacketTypes, RetainStatus: in.RetainStatus,
				TLSEnabled: in.TLSEnabled, TLSInsecure: in.TLSInsecure, AuthType: in.AuthType,
				Username: in.Username, Path: in.Path, Audience: in.Audience,
			}
			switch {
			case in.Password != nil:
				row.Password = *in.Password
			case in.ID != 0:
				for _, x := range rows.brokers {
					if x.ID == in.ID {
						row.Password = x.Password
					}
				}
			}
			if in.ID == 0 {
				rows.brokers = append(rows.brokers, row)
			} else {
				for i := range rows.brokers {
					if rows.brokers[i].ID == in.ID {
						rows.brokers[i] = row
					}
				}
			}
			return nil
		},
		func(st *store.Store) error {
			if row.ID == 0 {
				return st.Brokers.Create(ctx, &row)
			}
			return st.Brokers.Update(ctx, &row)
		},
	)
	return row.ID, err
}

func (b *backend) DeleteBroker(ctx context.Context, id int64) error {
	return b.configMutate(ctx,
		func(rows *configRows) error {
			rows.brokers = filterOut(rows.brokers, func(x store.Broker) bool { return x.ID == id })
			return nil
		},
		func(st *store.Store) error { return st.Brokers.Delete(ctx, id) },
	)
}

func (b *backend) SaveCompanion(ctx context.Context, in api.CompanionInput) (int64, error) {
	if err := config.RequireScope(in.FloodScope, true); err != nil {
		return 0, err
	}
	if in.ShareLocation == nil {
		return 0, api.Invalid(errors.New("shareLocation is required"))
	}
	// Resolve the key up front so a generation failure surfaces before the tx.
	key := ""
	if in.PrivateKey != nil {
		key = *in.PrivateKey
	}
	if in.ID == 0 && key == "" {
		k, err := config.GenerateSeedHex()
		if err != nil {
			return 0, err
		}
		key = k
	}

	var row store.Companion
	var newPublic *store.CompanionChannel
	err := b.configMutate(ctx,
		func(rows *configRows) error {
			if err := missingRow(rows.companions, in.ID, func(c store.Companion) int64 { return c.ID }, "companion"); err != nil {
				return err
			}
			prev := ""
			if i := slices.IndexFunc(rows.companions, func(c store.Companion) bool { return c.ID == in.ID }); in.ID != 0 && i >= 0 {
				prev = rows.companions[i].FloodScope
			}
			if err := listedScope(in.FloodScope, prev, rows.settings.FloodRegions); err != nil {
				return err
			}
			row = store.Companion{
				ID: in.ID, Name: in.Name,
				Latitude: in.Latitude, Longitude: in.Longitude, AdvertInterval: in.AdvertInterval,
				PathHashSize: in.PathHashSize,
				DMPolicy:     in.DMPolicy, DMAllow: in.DMAllow,
				FloodScope: in.FloodScope, ShareLocation: *in.ShareLocation,
			}
			row.PrivateKey = key
			// Telemetry modes have their own endpoint, so an edit from any other form must carry them through.
			for _, c := range rows.companions {
				if c.ID != in.ID || in.ID == 0 {
					continue
				}
				if row.PrivateKey == "" { // update without a key change → keep existing
					row.PrivateKey = c.PrivateKey
				}
				row.TelemBase, row.TelemLoc, row.TelemEnv = c.TelemBase, c.TelemLoc, c.TelemEnv
				row.AppEnabled, row.AppPort = c.AppEnabled, c.AppPort
			}
			row.PubKey, _ = config.PubKeyHexFromSeed(row.PrivateKey)

			if in.ID == 0 {
				rows.companions = append(rows.companions, row)
				// Every companion is always joined to Public.
				newPublic = &store.CompanionChannel{Name: "Public", FloodScope: string(config.ScopeInherit)}
				rows.channels = append(rows.channels, *newPublic)
			} else {
				for i := range rows.companions {
					if rows.companions[i].ID == in.ID {
						rows.companions[i] = row
					}
				}
			}
			return nil
		},
		func(st *store.Store) error {
			if row.ID != 0 {
				return st.Companions.Update(ctx, &row)
			}
			if err := st.Companions.Create(ctx, &row); err != nil {
				return err
			}
			newPublic.CompanionID = row.ID
			return st.Channels.Create(ctx, newPublic)
		},
	)
	return row.ID, err
}

// SetCompanionTelemetry is its own endpoint, or every other companion form would have to carry the modes.
func (b *backend) SetCompanionTelemetry(ctx context.Context, id int64, in api.CompanionTelemetryInput) error {
	modes := []string{in.Base, in.Location, in.Environment}
	for _, m := range modes {
		switch m {
		case config.TelemetryDeny, config.TelemetrySelected, config.TelemetryContacts:
		default:
			return fmt.Errorf("%q is not a telemetry mode", m)
		}
	}
	var row store.Companion
	err := b.configMutate(ctx,
		func(rows *configRows) error {
			if err := missingRow(rows.companions, id, func(c store.Companion) int64 { return c.ID }, "companion"); err != nil {
				return err
			}
			for i := range rows.companions {
				if rows.companions[i].ID != id {
					continue
				}
				rows.companions[i].TelemBase = in.Base
				rows.companions[i].TelemLoc = in.Location
				rows.companions[i].TelemEnv = in.Environment
				row = rows.companions[i]
			}
			return nil
		},
		func(st *store.Store) error {
			return st.Companions.Update(ctx, &row)
		},
	)
	return err
}

// SetCompanionApp is its own endpoint so that access is only ever turned on deliberately, never by saving another form.
func (b *backend) SetCompanionApp(ctx context.Context, id int64, in api.CompanionAppInput) error {
	if in.Enabled == nil || in.Port == nil {
		return api.Invalid(errors.New("enabled and port are both required"))
	}
	var row store.Companion
	return b.configMutate(ctx,
		func(rows *configRows) error {
			if err := missingRow(rows.companions, id, func(c store.Companion) int64 { return c.ID }, "companion"); err != nil {
				return err
			}
			for i := range rows.companions {
				if rows.companions[i].ID == id {
					rows.companions[i].AppEnabled, rows.companions[i].AppPort = *in.Enabled, *in.Port
					row = rows.companions[i]
				}
			}
			return nil
		},
		func(st *store.Store) error { return st.Companions.Update(ctx, &row) },
	)
}

func (b *backend) DeleteCompanion(ctx context.Context, id int64) error {
	return b.configMutate(ctx,
		func(rows *configRows) error {
			if rows.mqtt.NodeKind != config.MqttNodeRepeater && rows.mqtt.NodeCompanionID != nil && *rows.mqtt.NodeCompanionID == id {
				if mqttFeeding(rows) {
					return errFeedsMqtt("this companion")
				}
				rows.mqtt.NodeCompanionID = nil
			}
			rows.companions = filterOut(rows.companions, func(c store.Companion) bool { return c.ID == id })
			rows.channels = filterOut(rows.channels, func(c store.CompanionChannel) bool { return c.CompanionID == id })
			rows.triggers = filterOut(rows.triggers, func(t store.Trigger) bool { return t.CompanionID == id })
			return nil
		},
		func(st *store.Store) error { return st.Companions.Delete(ctx, id) }, // cascade clears children
	)
}

func (b *backend) SaveChannel(ctx context.Context, in api.ChannelInput) (int64, error) {
	if err := config.RequireScope(in.FloodScope, true); err != nil {
		return 0, err
	}
	var row store.CompanionChannel
	err := b.configMutate(ctx,
		func(rows *configRows) error {
			prevScope := ""
			if i := slices.IndexFunc(rows.channels, func(c store.CompanionChannel) bool { return c.ID == in.ID }); in.ID != 0 && i >= 0 {
				prevScope = rows.channels[i].FloodScope
			}
			if err := listedScope(in.FloodScope, prevScope, rows.settings.FloodRegions); err != nil {
				return err
			}
			row = store.CompanionChannel{ID: in.ID, CompanionID: in.CompanionID, Name: in.Name, FloodScope: in.FloodScope}
			if in.PrivateKey != nil {
				row.PrivateKey = *in.PrivateKey
			}
			if in.ID == 0 {
				if err := config.CheckChannelName(in.Name); err != nil {
					return api.Invalid(err)
				}
				if n := len(slices.DeleteFunc(slices.Clone(rows.channels), func(c store.CompanionChannel) bool { return c.CompanionID != in.CompanionID })); n >= node.DefaultMaxChannels {
					return api.Invalid(fmt.Errorf("the companion already has %d channels, all the node holds; remove one first", n))
				}
				rows.channels = append(rows.channels, row)
				return nil
			}
			i := slices.IndexFunc(rows.channels, func(c store.CompanionChannel) bool { return c.ID == in.ID })
			if i < 0 {
				return api.Failed(http.StatusNotFound, fmt.Errorf("channel %d not found", in.ID))
			}
			prev := rows.channels[i]
			if in.Name != prev.Name {
				return api.Invalid(errors.New("a channel is renamed from its thread, not here"))
			}
			row.CompanionID = prev.CompanionID
			if in.PrivateKey == nil {
				row.PrivateKey = prev.PrivateKey
			}
			rows.channels[i] = row
			return nil
		},
		func(st *store.Store) error {
			if row.ID == 0 {
				return st.Channels.Create(ctx, &row)
			}
			return st.Channels.Update(ctx, &row)
		},
	)
	return row.ID, err
}

func (b *backend) DeleteChannel(ctx context.Context, id int64) error {
	return b.configMutate(ctx,
		func(rows *configRows) error {
			rows.channels = filterOut(rows.channels, func(c store.CompanionChannel) bool { return c.ID == id })
			return nil
		},
		func(st *store.Store) error { return st.Channels.Delete(ctx, id) }, // cascade clears trigger links
	)
}

func (b *backend) SaveTrigger(ctx context.Context, in api.TriggerInput) (int64, error) {
	if err := config.RequireScope(in.FloodScope, true); err != nil {
		return 0, api.Invalid(err)
	}
	row := store.Trigger{
		ID: in.ID, CompanionID: in.CompanionID, Type: in.Type, Template: in.Template,
		CharLimitBehaviour: in.CharLimitBehaviour, MatchPatterns: in.Match, Contacts: in.Contacts,
		RetryTimeout: in.RetryTimeout, MaxRetries: in.MaxRetries, PathHashSize: in.PathHashSize,
		Schedule: in.Schedule, URL: in.URL, ChannelIDs: in.ChannelIDs,
		FailoverPattern: in.FailoverPattern, FailoverTimeout: in.FailoverTimeout,
		Regions: in.Regions, FloodScope: in.FloodScope,
	}
	loc, err := locationFromAPI(in.Location)
	if err != nil {
		return 0, api.Invalid(err)
	}
	row.Location = locationToStore(loc)
	if strings.TrimSpace(in.Template) == "" {
		return 0, api.Invalid(errors.New("template is required"))
	}
	if in.Type == "cron" && len(in.ChannelIDs) == 0 {
		return 0, api.Invalid(errors.New("cron trigger requires at least one channel"))
	}
	err = b.configMutate(ctx,
		func(rows *configRows) error {
			if err := missingRow(rows.triggers, in.ID, func(t store.Trigger) int64 { return t.ID }, "bot"); err != nil {
				return err
			}
			prev := ""
			if i := slices.IndexFunc(rows.triggers, func(t store.Trigger) bool { return t.ID == in.ID }); in.ID != 0 && i >= 0 {
				prev = rows.triggers[i].FloodScope
			}
			if err := listedScope(in.FloodScope, prev, rows.settings.FloodRegions); err != nil {
				return err
			}
			for _, id := range in.ChannelIDs {
				if !slices.ContainsFunc(rows.channels, func(c store.CompanionChannel) bool { return c.ID == id && c.CompanionID == in.CompanionID }) {
					return api.Invalid(fmt.Errorf("channel %d is not one of this companion's channels", id))
				}
			}
			if in.ID == 0 {
				rows.triggers = append(rows.triggers, row)
			} else {
				for i := range rows.triggers {
					if rows.triggers[i].ID == in.ID {
						rows.triggers[i] = row
					}
				}
			}
			return nil
		},
		func(st *store.Store) error {
			if row.ID == 0 {
				return st.Triggers.Create(ctx, &row)
			}
			return st.Triggers.Update(ctx, &row)
		},
	)
	return row.ID, err
}

// CreateRepeater seeds the "*" wildcard scope so the repeater relays unscoped flood, as the firmware implicitly does.
func (b *backend) CreateRepeater(ctx context.Context, in api.RepeaterCreateInput) error {
	if strings.TrimSpace(in.AdminPassword) == "" {
		return errors.New("admin password is required: a blank one grants admin to any node in range")
	}

	var row store.Repeater
	return b.configMutate(ctx,
		func(rows *configRows) error {
			if rows.repeater != nil {
				return errors.New("repeater already configured")
			}
			row = store.Repeater{
				Name:          in.Name,
				AdminPassword: in.AdminPassword,
				Regions:       []store.RepeaterRegion{{Name: config.WildcardRegion}},
				FloodScope:    string(config.ScopeEverywhere),
			}
			if in.PrivateKey != nil && *in.PrivateKey != "" {
				row.PrivateKey = *in.PrivateKey
			} else {
				row.PrivateKey, _ = config.GenerateSeedHex()
			}
			row.PubKey, _ = config.PubKeyHexFromSeed(row.PrivateKey)
			rows.repeater = &row
			return nil
		},
		func(st *store.Store) error {
			return st.Repeater.Set(ctx, &row)
		},
	)
}

// mutateRepeater applies a partial change through one validated, reloaded write; it errors when no repeater exists.
func (b *backend) mutateRepeater(ctx context.Context, mutate func(*store.Repeater) error) error {
	var row store.Repeater
	return b.configMutate(ctx,
		func(rows *configRows) error {
			if rows.repeater == nil {
				return errors.New("no repeater configured")
			}
			row = *rows.repeater
			if err := mutate(&row); err != nil {
				return err
			}
			rows.repeater = &row
			return nil
		},
		func(st *store.Store) error {
			return st.Repeater.Set(ctx, &row)
		},
	)
}

// UpdateRepeaterNode edits the Node section; a supplied key value rotates the identity.
func (b *backend) UpdateRepeaterNode(ctx context.Context, in api.RepeaterNodeInput) error {
	return b.mutateRepeater(ctx, func(r *store.Repeater) error {
		r.Name = in.Name
		r.Latitude = in.Latitude
		r.Longitude = in.Longitude
		if in.PrivateKey != nil && *in.PrivateKey != "" {
			r.PrivateKey = *in.PrivateKey
			r.PubKey, _ = config.PubKeyHexFromSeed(r.PrivateKey)
		}
		return nil
	})
}

// UpdateRepeaterRelay edits the Relay-policy section.
func (b *backend) UpdateRepeaterRelay(ctx context.Context, in api.RepeaterRelayInput) error {
	return b.mutateRepeater(ctx, func(r *store.Repeater) error {
		r.DisableFwd = in.DisableFwd
		r.FloodMax = in.FloodMax
		r.FloodMaxUnscoped = in.FloodMaxUnscoped
		r.FloodMaxAdvert = in.FloodMaxAdvert
		r.LoopDetect = in.LoopDetect
		r.PathHashSize = in.PathHashSize
		r.TxDelayFactor = in.TxDelayFactor
		r.DirectTxDelayFactor = in.DirectTxDelayFactor
		r.RxDelayBase = in.RxDelayBase
		r.MultiAcks = in.MultiAcks
		r.AdvertInterval = in.AdvertInterval
		r.FloodAdvertInterval = in.FloodAdvertInterval
		return nil
	})
}

// UpdateRepeaterAdmin edits the Owner & access section (nil password = keep).
func (b *backend) UpdateRepeaterAdmin(ctx context.Context, in api.RepeaterAdminInput) error {
	// Omitting the field keeps the stored value; sending "" would clear it, and a blank admin
	// password compares equal to the blank a login sends. Guest may still be cleared: blank guest
	// grants PERM_ACL_GUEST (0), which is what the firmware does.
	if in.AdminPassword != nil && strings.TrimSpace(*in.AdminPassword) == "" {
		return errors.New("admin password cannot be blank: omit the field to keep the current one")
	}
	return b.mutateRepeater(ctx, func(r *store.Repeater) error {
		r.OwnerInfo = in.OwnerInfo
		r.AdminPassword = keepSecret(in.AdminPassword, r.AdminPassword)
		r.GuestPassword = keepSecret(in.GuestPassword, r.GuestPassword)
		return nil
	})
}

// SetRepeaterFloodScope is `region default`: the scope names one of its regions, whose flood it allows, or everywhere.
func (b *backend) SetRepeaterFloodScope(ctx context.Context, in api.RepeaterScopeInput) error {
	if err := config.RequireScope(in.FloodScope, false); err != nil {
		return err
	}
	return b.mutateRepeater(ctx, func(r *store.Repeater) error {
		r.FloodScope = in.FloodScope
		for i := range r.Regions {
			if config.FloodScope(in.FloodScope).NamesRegion(r.Regions[i].Name) {
				r.Regions[i].DenyFlood = false // firmware: def->flags = 0
			}
		}
		return nil
	})
}

// SetRepeaterHome sets the home region, "*" for none, as `region home` does.
func (b *backend) SetRepeaterHome(ctx context.Context, in api.RepeaterHomeInput) error {
	if in.Region == "" {
		return errors.New(`region is required: "*" for none`)
	}
	home := in.Region
	if home == config.WildcardRegion {
		home = "" // home_id 0 is the wildcard
	}
	return b.mutateRepeater(ctx, func(r *store.Repeater) error {
		if home != "" {
			home = storedRegionName(r.Regions, home) // the firmware finds it with any "#" ignored; the stored name is kept
		}
		r.HomeRegion = home
		return nil
	})
}

// AddRepeaterRegion adds a new region; one that exists, "#" ignored as the firmware's lookup does, is moved with MoveRepeaterRegion instead.
func (b *backend) AddRepeaterRegion(ctx context.Context, in api.RepeaterRegionInput) error {
	if in.Parent == nil {
		return errors.New(`parent is required: "*" for the top, or the region it sits under`)
	}
	if in.Name == config.WildcardRegion {
		return errors.New(`"*" is the top region and always exists`)
	}
	return b.mutateRepeater(ctx, func(r *store.Repeater) error {
		for _, rg := range r.Regions {
			if config.SameRegionName(rg.Name, in.Name) {
				return api.Failed(http.StatusConflict, fmt.Errorf("region %q already exists", rg.Name))
			}
		}
		r.Regions = append(r.Regions, store.RepeaterRegion{Name: in.Name, Parent: storedRegionName(r.Regions, *in.Parent), DenyFlood: in.DenyFlood})
		return nil
	})
}

// MoveRepeaterRegion puts a region under another; "*" stays at the top, and a loop is refused by Validate.
func (b *backend) MoveRepeaterRegion(ctx context.Context, name, parent string) error {
	if name == config.WildcardRegion {
		return errors.New(`"*" is the top region and cannot be moved`)
	}
	return b.mutateRepeater(ctx, func(r *store.Repeater) error {
		for i := range r.Regions {
			if config.SameRegionName(r.Regions[i].Name, name) {
				r.Regions[i].Parent = storedRegionName(r.Regions, parent)
				return nil
			}
		}
		return api.Failed(http.StatusNotFound, fmt.Errorf("unknown region %q", name))
	})
}

// storedRegionName is the region's name as stored, found as the firmware does with any "#" ignored; an unknown name is returned as given.
func storedRegionName(regions []store.RepeaterRegion, name string) string {
	for _, rg := range regions {
		if config.SameRegionName(rg.Name, name) {
			return rg.Name
		}
	}
	return name
}

// SetRepeaterRegionFlood toggles a region's deny-flood flag; "*" always exists, so it is added if it has no entry.
func (b *backend) SetRepeaterRegionFlood(ctx context.Context, name string, denyFlood bool) error {
	return b.mutateRepeater(ctx, func(r *store.Repeater) error {
		for i := range r.Regions {
			if config.SameRegionName(r.Regions[i].Name, name) {
				r.Regions[i].DenyFlood = denyFlood
				return nil
			}
		}
		if name == config.WildcardRegion {
			r.Regions = append(r.Regions, store.RepeaterRegion{Name: name, DenyFlood: denyFlood})
			return nil
		}
		return api.Failed(http.StatusNotFound, fmt.Errorf("unknown region %q", name))
	})
}

// RemoveRepeaterRegion refuses "*", as the firmware does; denying flood on it stops relaying unscoped flood.
func (b *backend) RemoveRepeaterRegion(ctx context.Context, name string) error {
	if name == config.WildcardRegion {
		return errors.New(`the "*" region cannot be removed; deny flood on it instead`)
	}
	return b.mutateRepeater(ctx, func(r *store.Repeater) error {
		if !slices.ContainsFunc(r.Regions, func(rg store.RepeaterRegion) bool { return config.SameRegionName(rg.Name, name) }) {
			return api.Failed(http.StatusNotFound, fmt.Errorf("unknown region %q", name))
		}
		for _, rg := range r.Regions {
			if config.SameRegionName(rg.Parent, name) {
				return fmt.Errorf("region %q has sub-regions (%s); remove or move them first", name, rg.Name)
			}
		}
		name := storedRegionName(r.Regions, name)
		r.Regions = slices.DeleteFunc(r.Regions, func(rg store.RepeaterRegion) bool { return rg.Name == name })
		if r.HomeRegion == name {
			r.HomeRegion = ""
		}
		if config.FloodScope(r.FloodScope).NamesRegion(name) {
			r.FloodScope = string(config.ScopeEverywhere) // the firmware's default_id no longer finds a region
		}
		return nil
	})
}

func (b *backend) DeleteRepeater(ctx context.Context) error {
	var mqtt *store.MqttSettings
	return b.configMutate(ctx,
		func(rows *configRows) error {
			if rows.mqtt.NodeKind == config.MqttNodeRepeater {
				if mqttFeeding(rows) {
					return errFeedsMqtt("the repeater")
				}
				m := *rows.mqtt
				m.NodeKind = config.MqttNodeCompanion
				rows.mqtt, mqtt = &m, &m
			}
			rows.repeater = nil
			return nil
		},
		func(st *store.Store) error {
			if mqtt != nil {
				if err := st.Mqtt.Set(ctx, mqtt); err != nil {
					return err
				}
			}
			if err := st.RepeaterACL.Clear(ctx); err != nil { // drop admin-over-mesh clients
				return err
			}
			return st.Repeater.Clear(ctx)
		},
	)
}

// keepSecret implements the "omit to keep" contract for redacted secret fields.
func keepSecret(in *string, prev string) string {
	if in != nil {
		return *in
	}
	return prev
}

func (b *backend) DeleteTrigger(ctx context.Context, id int64) error {
	return b.configMutate(ctx,
		func(rows *configRows) error {
			rows.triggers = filterOut(rows.triggers, func(t store.Trigger) bool { return t.ID == id })
			return nil
		},
		func(st *store.Store) error { return st.Triggers.Delete(ctx, id) },
	)
}
