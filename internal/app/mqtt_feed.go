package app

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"slices"

	meshcore "github.com/OwlShack/meshcore-go"
	"github.com/OwlShack/meshcore-go/node"

	"github.com/OwlShack/OwlShack/internal/config"
	"github.com/OwlShack/OwlShack/internal/modem"
	"github.com/OwlShack/OwlShack/internal/mqtt"
	"github.com/OwlShack/OwlShack/internal/node/companion"
	"github.com/OwlShack/OwlShack/internal/node/repeater"
)

// mqttFeed is the one MQTT observer, speaking as the node the MQTT settings pick; key is what it was built from.
type mqttFeed struct {
	obs *mqtt.Observer
	key string
}

// feedingNode is the node MQTT speaks as: the repeater or the named companion; ok=false when it isn't running or none is named.
func feedingNode(cfg *config.Config, companions []*companion.Companion, rep *repeater.Repeater) (string, meshcore.LocalIdentity, bool) {
	if cfg.Mqtt.FedByRepeater() {
		if rep == nil {
			return "", meshcore.LocalIdentity{}, false
		}
		return rep.Name(), rep.Node().Identity(), true
	}
	if cfg.Mqtt == nil || cfg.Mqtt.Node == nil || *cfg.Mqtt.Node == "" {
		return "", meshcore.LocalIdentity{}, false
	}
	for _, c := range companions {
		if c.Name() == *cfg.Mqtt.Node {
			return c.Name(), c.Node().Identity(), true
		}
	}
	return "", meshcore.LocalIdentity{}, false
}

// feedKey changes whenever the observer must be rebuilt: its settings and enabled brokers, or the name and key it speaks as.
func feedKey(cfg *config.Config, name string, id meshcore.LocalIdentity) string {
	m := *cfg.Mqtt
	m.Brokers = slices.DeleteFunc(slices.Clone(m.Brokers), func(b config.BrokerConfig) bool { return !b.Enabled })
	j, err := json.Marshal(m)
	if err != nil {
		return "" // never equal to a running feed's key, so it rebuilds
	}
	return string(j) + "\x00" + name + "\x00" + hex.EncodeToString(id.PublicKeyBytes())
}

// reloadMqtt keeps the running feed when nothing it was built from changed, so its counters and connections survive; a feed that can't start is logged, not fatal.
func reloadMqtt(ctx context.Context, cfg *config.Config, companions []*companion.Companion, rep *repeater.Repeater, mux *node.RadioMux, ms *modem.State, running *mqttFeed) *mqttFeed {
	relaying := cfg.Repeater != nil && !cfg.Repeater.IsFwdDisabled()
	if !cfg.Mqtt.Publishes() {
		running.stop()
		return nil
	}
	name, id, ok := feedingNode(cfg, companions, rep)
	if !ok {
		slog.Error("mqtt is on but the node it speaks as isn't running, so nothing is published")
		running.stop()
		return nil
	}
	key := feedKey(cfg, name, id)
	if running != nil && running.key == key && key != "" {
		running.obs.SetRelaying(relaying)
		return running
	}
	running.stop()
	obs, err := mqtt.NewObserver(*cfg.Mqtt, name, mux, id, statsOf(ms), ms.ParseErrors)
	if err != nil {
		slog.Error("creating the mqtt observer", "node", name, "error", err)
		return nil
	}
	obs.SetRelaying(relaying)
	if err := obs.Start(ctx); err != nil {
		slog.Error("starting the mqtt observer", "node", name, "error", err)
		return nil
	}
	slog.Info("mqtt observer started", "node", name)
	return &mqttFeed{obs: obs, key: key}
}

func (f *mqttFeed) stop() {
	if f != nil {
		f.obs.Stop()
	}
}

// observer is nil-safe, for the backend and the packet logger.
func (f *mqttFeed) observer() *mqtt.Observer {
	if f == nil {
		return nil
	}
	return f.obs
}
