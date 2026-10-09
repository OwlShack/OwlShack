package app

import (
	"context"
	"log/slog"

	"github.com/OwlShack/OwlShack/internal/api"
	"sync"
	"time"

	"github.com/OwlShack/OwlShack/internal/companionapp"
	"github.com/OwlShack/OwlShack/internal/config"
	"github.com/OwlShack/OwlShack/internal/modem"
	"github.com/OwlShack/OwlShack/internal/node/companion"
	"github.com/OwlShack/OwlShack/internal/store"
	"github.com/OwlShack/meshcore-go/node"
)

// appServers is one MeshCore app listener per companion with app access on; it follows reloads and goes down with the radio.
type appServers struct {
	host     string
	st       *store.Store
	started  time.Time
	settings companionapp.Settings

	mu   sync.Mutex
	byID map[int64]*companionapp.Server
}

func newAppServers(host string, st *store.Store, settings companionapp.Settings) *appServers {
	return &appServers{host: host, st: st, started: time.Now(), settings: settings, byID: map[int64]*companionapp.Server{}}
}

// sync opens, moves or closes each port to match cfg, and rebinds a kept port to its companion, which a reload may have rebuilt.
func (a *appServers) sync(cfg *config.Config, companions []*companion.Companion, ms *modem.State, mux *node.RadioMux) {
	regions := make([]string, 0, len(cfg.FloodRegions))
	for _, r := range cfg.FloodRegions {
		regions = append(regions, r.Name)
	}
	env := companionapp.Env{Radio: ms.Stats.RadioConfig(), AirtimeFactor: cfg.AirtimeFactorOr(), Regions: regions, Stats: ms.Stats, Tx: mux, Started: a.started}
	ports := map[int64]int{}
	for _, cc := range cfg.Companions {
		if cc.App.Enabled {
			ports[cc.ID] = cc.App.Port
		}
	}
	running := map[int64]*companion.Companion{}
	for _, c := range companions {
		running[c.ID()] = c
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	for id, s := range a.byID {
		// A port that failed to open is tried again on every reload.
		if port, ok := ports[id]; !ok || port != s.Port() || running[id] == nil || s.Status().Error != "" {
			s.Stop()
			delete(a.byID, id)
		}
	}
	for id, port := range ports {
		c := running[id]
		if c == nil {
			continue
		}
		if s, ok := a.byID[id]; ok {
			s.SetEnv(env)
			s.Use(c)
			continue
		}
		a.byID[id] = companionapp.Start(a.host, port, c, env, a.st, a.settings, slog.Default())
	}
}

func (a *appServers) stop() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for id, s := range a.byID {
		s.Stop()
		delete(a.byID, id)
	}
}

func (a *appServers) server(id int64) *companionapp.Server {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.byID[id]
}

func (b *backend) CompanionApp(name string) (api.CompanionAppStatus, bool) {
	c, ok := b.find(name)
	if !ok {
		return api.CompanionAppStatus{}, false
	}
	s := b.apps.server(c.ID())
	if s == nil {
		return api.CompanionAppStatus{}, true
	}
	st := s.Status()
	out := api.CompanionAppStatus{Enabled: true, Port: st.Port, Listening: st.Listening, Error: st.Error, Client: st.Client}
	if st.Client != "" {
		out.Since = st.Since.UTC().Format(time.RFC3339)
	}
	return out, true
}

func (b *backend) DisconnectCompanionApp(name string) bool {
	c, ok := b.find(name)
	if !ok {
		return false
	}
	s := b.apps.server(c.ID())
	if s == nil {
		return false
	}
	s.Disconnect()
	return true
}

// UpdateCompanion changes a companion's own settings for the MeshCore app, through the checks and reload a save from the UI takes.
func (b *backend) UpdateCompanion(ctx context.Context, id int64, change func(*store.Companion) error) error {
	var row store.Companion
	return b.configMutate(ctx,
		func(rows *configRows) error {
			if err := missingRow(rows.companions, id, func(c store.Companion) int64 { return c.ID }, "companion"); err != nil {
				return err
			}
			for i := range rows.companions {
				if rows.companions[i].ID != id {
					continue
				}
				if err := change(&rows.companions[i]); err != nil {
					return err
				}
				row = rows.companions[i]
			}
			return nil
		},
		func(st *store.Store) error { return st.Companions.Update(ctx, &row) },
	)
}

// SaveChannels keeps the channels the app set, as the Channels page keeps its own.
func (b *backend) SaveChannels(ctx context.Context, c *companion.Companion) error {
	return persistChannels(ctx, b.db, []*companion.Companion{c}, true)
}
