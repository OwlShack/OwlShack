package app

import (
	"bytes"
	"encoding/hex"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/OwlShack/meshcore-go/node"

	"github.com/OwlShack/OwlShack/internal/config"
	"github.com/OwlShack/OwlShack/internal/modem"
	"github.com/OwlShack/OwlShack/internal/node/companion"
	"github.com/OwlShack/OwlShack/internal/node/repeater"
)

// The feed speaks as the node the settings pick, and is rebuilt only when that or its settings change.
func TestMqttFeed_SpeaksAsThePickedNode(t *testing.T) {
	b := newHealthBackend(t)
	mux := node.NewRadioMux(silentModem{})
	home, err := companion.NewCompanion(config.CompanionConfig{Name: "home", PrivateKey: strings.Repeat("11", 32)}, mux, b.db, nil, nil, nil, floodScopeOf)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := repeater.NewRepeater(config.RepeaterConfig{Name: "rp", PrivateKey: strings.Repeat("22", 32), AdminPassword: "pw"}, mux, b.db, nil, repeater.Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	companions := []*companion.Companion{home}
	kind := config.MqttNodeRepeater
	cfg := &config.Config{Mqtt: &config.MqttConfig{NodeKind: &kind, Brokers: []config.BrokerConfig{{Name: "x", Enabled: true, Host: "127.0.0.1", Port: 1}}}}

	name, id, ok := feedingNode(cfg, companions, rep)
	if !ok || name != "rp" || !bytes.Equal(id.PublicKeyBytes(), rep.Node().Identity().PublicKeyBytes()) {
		t.Errorf("repeater feed speaks as %q %s, want rp with its key", name, hex.EncodeToString(id.PublicKeyBytes()))
	}
	if _, _, ok := feedingNode(cfg, companions, nil); ok {
		t.Error("a repeater feed with no repeater running still found a node")
	}

	ms := &modem.State{ParseErrors: new(atomic.Uint64)}
	feed := reloadMqtt(t.Context(), cfg, companions, rep, mux, ms, nil)
	t.Cleanup(func() { feed.stop() })
	if feed == nil {
		t.Fatal("no feed started for the repeater")
	}
	if topic := feed.obs.BrokerStatuses()[0].StatusTopic; !strings.Contains(strings.ToUpper(topic), strings.ToUpper(hex.EncodeToString(rep.Node().Identity().PublicKeyBytes()))) {
		t.Errorf("the repeater feed publishes status on %q, not under the repeater's key", topic)
	}
	if again := reloadMqtt(t.Context(), cfg, companions, rep, mux, ms, feed); again != feed {
		t.Error("an unchanged reload rebuilt the feed, dropping its connections and counts")
	}
	cfg.Mqtt.Brokers = append(cfg.Mqtt.Brokers, config.BrokerConfig{Name: "off", Host: "127.0.0.1", Port: 2})
	if again := reloadMqtt(t.Context(), cfg, companions, rep, mux, ms, feed); again != feed {
		t.Error("adding a disabled broker rebuilt the feed, dropping its connections and counts")
	}
	cfg.Repeater = &config.RepeaterConfig{}
	if again := reloadMqtt(t.Context(), cfg, companions, rep, mux, ms, feed); again != feed || !feed.obs.Relaying() {
		t.Error("turning repeat on kept the reused feed's status saying it doesn't relay")
	}
	off := true
	cfg.Repeater.DisableFwd = &off
	if again := reloadMqtt(t.Context(), cfg, companions, rep, mux, ms, feed); again != feed || feed.obs.Relaying() {
		t.Error("turning repeat off kept the reused feed's status saying it relays")
	}

	if f := reloadMqtt(t.Context(), &config.Config{Mqtt: &config.MqttConfig{Brokers: cfg.Mqtt.Brokers}}, companions, rep, mux, ms, nil); f != nil {
		f.stop()
		t.Error("a companion feed naming no companion started, speaking as one nobody picked")
	}
	homeName := "home"
	companionCfg := &config.Config{Mqtt: &config.MqttConfig{Node: &homeName, Brokers: cfg.Mqtt.Brokers}}
	moved := reloadMqtt(t.Context(), companionCfg, companions, rep, mux, ms, feed)
	feed = moved
	if moved == nil || moved.key == "" || strings.Contains(moved.key, hex.EncodeToString(rep.Node().Identity().PublicKeyBytes())) {
		t.Errorf("after picking the companion the feed still speaks as the repeater")
	}
	if !strings.Contains(moved.key, hex.EncodeToString(home.Node().Identity().PublicKeyBytes())) {
		t.Error("the feed doesn't speak as the companion after picking it")
	}
}

// Most installs have no MQTT settings at all; that must start no feed, not crash the startup.
func TestMqttFeed_NoMqttSettings(t *testing.T) {
	mux := node.NewRadioMux(silentModem{})
	home, err := companion.NewCompanion(config.CompanionConfig{Name: "home", PrivateKey: strings.Repeat("11", 32)}, mux, newHealthBackend(t).db, nil, nil, nil, floodScopeOf)
	if err != nil {
		t.Fatal(err)
	}
	ms := &modem.State{ParseErrors: new(atomic.Uint64)}
	for _, cfg := range []*config.Config{{}, {Mqtt: &config.MqttConfig{}}} {
		if feed := reloadMqtt(t.Context(), cfg, []*companion.Companion{home}, nil, mux, ms, nil); feed != nil {
			t.Errorf("mqtt %+v started a feed", cfg.Mqtt)
		}
	}
}

// With every broker disabled there is nowhere to publish, so no feed runs.
func TestMqttFeed_AllBrokersDisabled(t *testing.T) {
	mux := node.NewRadioMux(silentModem{})
	home, err := companion.NewCompanion(config.CompanionConfig{Name: "home", PrivateKey: strings.Repeat("11", 32)}, mux, newHealthBackend(t).db, nil, nil, nil, floodScopeOf)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Mqtt: &config.MqttConfig{Brokers: []config.BrokerConfig{{Name: "x", Enabled: false, Host: "127.0.0.1", Port: 1}}}}
	ms := &modem.State{ParseErrors: new(atomic.Uint64)}
	if feed := reloadMqtt(t.Context(), cfg, []*companion.Companion{home}, nil, mux, ms, nil); feed != nil {
		feed.stop()
		t.Error("a feed started with every broker disabled")
	}
}
