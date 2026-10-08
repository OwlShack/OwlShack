package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/OwlShack/OwlShack/internal/api"
	"github.com/OwlShack/OwlShack/internal/config"
)

// mqttBackend stores a valid config of one companion, a broker and optionally a repeater, returning the companion's id.
func mqttBackend(t *testing.T, withRepeater bool) (*backend, int64) {
	t.Helper()
	b := newHealthBackend(t)
	cfg := config.DefaultConfig()
	seed, err := config.GenerateSeedHex()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Companions = []config.CompanionConfig{{Name: "one", PrivateKey: seed}}
	one := "one"
	cfg.Mqtt = &config.MqttConfig{Node: &one, Brokers: []config.BrokerConfig{{Name: "local", Enabled: true, Host: "localhost", Port: 1883}}}
	if withRepeater {
		cfg.Repeater = &config.RepeaterConfig{Name: "rp", AdminPassword: "pw", Regions: []config.RepeaterRegion{{Name: "*"}}}
	}
	if err := saveConfig(t.Context(), b.db, &cfg); err != nil {
		t.Fatal(err)
	}
	comps, err := b.db.Companions.List(t.Context())
	if err != nil || len(comps) != 1 {
		t.Fatalf("companions = %v, %v", comps, err)
	}
	return b, comps[0].ID
}

func statusOf(err error) int {
	if verr := (*api.ValidationError)(nil); errors.As(err, &verr) {
		return http.StatusUnprocessableEntity
	}
	if serr := (*api.StatusError)(nil); errors.As(err, &serr) {
		return serr.Status
	}
	if err != nil {
		return http.StatusInternalServerError
	}
	return http.StatusOK
}

// The MQTT save names the kind of node explicitly, and only one that exists.
func TestSaveMqtt_FeedingNode(t *testing.T) {
	t.Parallel()
	on := true
	b, c1 := mqttBackend(t, false)
	missing := int64(9999)
	for _, tc := range []struct {
		name string
		in   api.MqttInput
		want int
	}{
		{"no kind", api.MqttInput{Enabled: &on, NodeCompanionID: &c1}, http.StatusUnprocessableEntity},
		{"unknown kind", api.MqttInput{Enabled: &on, NodeKind: "bridge"}, http.StatusUnprocessableEntity},
		{"repeater with none configured", api.MqttInput{Enabled: &on, NodeKind: "repeater"}, http.StatusUnprocessableEntity},
		{"companion that doesn't exist", api.MqttInput{Enabled: &on, NodeKind: "companion", NodeCompanionID: &missing}, http.StatusUnprocessableEntity},
		{"companion without naming one", api.MqttInput{Enabled: &on, NodeKind: "companion"}, http.StatusUnprocessableEntity},
		{"companion", api.MqttInput{Enabled: &on, NodeKind: "companion", NodeCompanionID: &c1}, http.StatusOK},
	} {
		if got := statusOf(b.SaveMqtt(t.Context(), tc.in)); got != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, got, tc.want)
		}
	}

	rb, rc1 := mqttBackend(t, true)
	if got := statusOf(rb.SaveMqtt(t.Context(), api.MqttInput{Enabled: &on, NodeKind: "repeater", NodeCompanionID: &rc1})); got != http.StatusUnprocessableEntity {
		t.Errorf("repeater with a companion id too: %d, want 422", got)
	}
	if err := rb.SaveMqtt(t.Context(), api.MqttInput{Enabled: &on, NodeKind: "repeater"}); err != nil {
		t.Fatalf("repeater: %v", err)
	}
	cfg, err := loadConfigFromDB(t.Context(), rb.db)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Mqtt.FedByRepeater() || cfg.Mqtt.Node != nil {
		t.Errorf("loaded mqtt = kind %v node %v, want the repeater", cfg.Mqtt.NodeKind, cfg.Mqtt.Node)
	}
}

// The node feeding MQTT can't be deleted while MQTT is on, so the feed never changes identity unasked; with MQTT off the choice is cleared.
func TestDelete_NodeFeedingMqtt(t *testing.T) {
	t.Parallel()
	on, off := true, false

	b, c1 := mqttBackend(t, true)
	if err := b.SaveMqtt(t.Context(), api.MqttInput{Enabled: &on, NodeKind: "companion", NodeCompanionID: &c1}); err != nil {
		t.Fatal(err)
	}
	if got := statusOf(b.DeleteCompanion(t.Context(), c1)); got != http.StatusConflict {
		t.Errorf("deleting the companion feeding MQTT: %d, want 409", got)
	}
	if err := b.SaveMqtt(t.Context(), api.MqttInput{Enabled: &on, NodeKind: "repeater"}); err != nil {
		t.Fatal(err)
	}
	if got := statusOf(b.DeleteRepeater(t.Context())); got != http.StatusConflict {
		t.Errorf("deleting the repeater feeding MQTT: %d, want 409", got)
	}
	if err := b.DeleteCompanion(t.Context(), c1); err != nil {
		t.Errorf("deleting a companion that doesn't feed MQTT: %v", err)
	}

	if err := b.SaveMqtt(t.Context(), api.MqttInput{Enabled: &off, NodeKind: "repeater"}); err != nil {
		t.Fatal(err)
	}
	if err := b.DeleteRepeater(t.Context()); err != nil {
		t.Fatalf("deleting the repeater with MQTT off: %v", err)
	}
	m, err := b.db.Mqtt.Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if m.NodeKind != config.MqttNodeCompanion {
		t.Errorf("after the repeater went, MQTT is fed by %q, want the choice cleared to companion", m.NodeKind)
	}
}

// With every broker disabled nothing publishes, so the feeding node is free to go.
func TestDelete_FeedingNodeWithBrokersOff(t *testing.T) {
	t.Parallel()
	on := true
	b, c1 := mqttBackend(t, false)
	if err := b.SaveMqtt(t.Context(), api.MqttInput{Enabled: &on, NodeKind: "companion", NodeCompanionID: &c1}); err != nil {
		t.Fatal(err)
	}
	brokers, err := b.db.Brokers.List(t.Context())
	if err != nil || len(brokers) != 1 {
		t.Fatalf("brokers = %v, %v", brokers, err)
	}
	br := brokers[0]
	if _, err := b.SaveBroker(t.Context(), api.BrokerInput{ID: br.ID, Name: br.Name, Enabled: false, Host: br.Host, Port: br.Port}); err != nil {
		t.Fatal(err)
	}
	if err := b.DeleteCompanion(t.Context(), c1); err != nil {
		t.Errorf("deleting the companion with every broker off: %v", err)
	}
}

// A config file that names no MQTT node is stored naming the first companion, so the choice is explicit from then on.
func TestImport_NamesTheFirstCompanion(t *testing.T) {
	t.Parallel()
	b := newHealthBackend(t)
	cfg := config.DefaultConfig()
	seed, err := config.GenerateSeedHex()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Companions = []config.CompanionConfig{{Name: "one", PrivateKey: seed}}
	cfg.Mqtt = &config.MqttConfig{Brokers: []config.BrokerConfig{{Name: "local", Enabled: true, Host: "localhost", Port: 1883}}}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := importConfigFile(t.Context(), b.db, path); err != nil {
		t.Fatal(err)
	}
	comps, err := b.db.Companions.List(t.Context())
	if err != nil || len(comps) != 1 {
		t.Fatalf("companions = %v, %v", comps, err)
	}
	m, err := b.db.Mqtt.Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if m.NodeCompanionID == nil || *m.NodeCompanionID != comps[0].ID {
		t.Fatalf("stored node = %v, want companion %d", m.NodeCompanionID, comps[0].ID)
	}
}

// A legacy config blob that names no MQTT node is moved into the tables naming the first companion, as a file import is.
func TestLegacyBlob_NamesTheFirstCompanion(t *testing.T) {
	t.Parallel()
	b := newHealthBackend(t)
	cfg := config.DefaultConfig()
	seed, err := config.GenerateSeedHex()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Companions = []config.CompanionConfig{{Name: "one", PrivateKey: seed}}
	cfg.Mqtt = &config.MqttConfig{Brokers: []config.BrokerConfig{{Name: "local", Enabled: true, Host: "localhost", Port: 1883}}}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var werr error
	b.db.WriteSync(func() { werr = b.db.AppConfig.Set(t.Context(), string(raw)) })
	if werr != nil {
		t.Fatal(werr)
	}
	if _, err := initConfigTables(t.Context(), b.db); err != nil {
		t.Fatal(err)
	}
	comps, err := b.db.Companions.List(t.Context())
	if err != nil || len(comps) != 1 {
		t.Fatalf("companions = %v, %v", comps, err)
	}
	m, err := b.db.Mqtt.Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if m.NodeCompanionID == nil || *m.NodeCompanionID != comps[0].ID {
		t.Fatalf("stored node = %v, want companion %d", m.NodeCompanionID, comps[0].ID)
	}
}

// Only an import names a node; a whole-config rewrite (the channel save) stores the feed as given, never picking one.
func TestWriteConfig_PicksNoFeedNode(t *testing.T) {
	t.Parallel()
	b, _ := mqttBackend(t, false)
	cfg, err := loadConfigFromDB(t.Context(), b.db)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Mqtt.Node = nil
	if err := saveConfig(t.Context(), b.db, cfg); err != nil {
		t.Fatal(err)
	}
	m, err := b.db.Mqtt.Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if m.NodeCompanionID != nil {
		t.Errorf("a rewrite picked companion %d to feed MQTT", *m.NodeCompanionID)
	}
}

// Turning on a broker can't leave MQTT publishing with no node, which would look on and send nothing.
func TestSaveBroker_NeedsAFeedNode(t *testing.T) {
	t.Parallel()
	b, c1 := mqttBackend(t, false)
	brokers, err := b.db.Brokers.List(t.Context())
	if err != nil || len(brokers) != 1 {
		t.Fatalf("brokers = %v, %v", brokers, err)
	}
	br := api.BrokerInput{ID: brokers[0].ID, Name: brokers[0].Name, Host: brokers[0].Host, Port: brokers[0].Port}
	if _, err := b.SaveBroker(t.Context(), br); err != nil {
		t.Fatal(err)
	}
	if err := b.DeleteCompanion(t.Context(), c1); err != nil {
		t.Fatal(err)
	}
	br.Enabled = true
	if _, err := b.SaveBroker(t.Context(), br); statusOf(err) != http.StatusUnprocessableEntity {
		t.Errorf("enabling a broker with no node to feed it: %v, want 422", err)
	}
	if _, err := b.SaveBroker(t.Context(), api.BrokerInput{Name: "two", Enabled: true, Host: "localhost", Port: 1884}); statusOf(err) != http.StatusUnprocessableEntity {
		t.Errorf("adding an enabled broker with no node to feed it: %v, want 422", err)
	}
	if _, err := b.SaveBroker(t.Context(), api.BrokerInput{Name: "two", Host: "localhost", Port: 1884}); err != nil {
		t.Errorf("adding a disabled broker: %v", err)
	}

	// A database already publishing with no node (an upgrade) can still be edited.
	var werr error
	b.db.WriteSync(func() {
		brokers[0].Enabled = true
		werr = b.db.Brokers.Update(t.Context(), &brokers[0])
	})
	if werr != nil {
		t.Fatal(werr)
	}
	if _, err := b.SaveBroker(t.Context(), api.BrokerInput{Name: "three", Host: "localhost", Port: 1885}); err != nil {
		t.Errorf("an edit that leaves the feed as it was: %v", err)
	}
}

// A companion-fed MQTT with no companion would publish nothing while looking on.
func TestSaveMqtt_CompanionFeedNeedsACompanion(t *testing.T) {
	t.Parallel()
	on := true
	b, c1 := mqttBackend(t, true)
	if err := b.SaveMqtt(t.Context(), api.MqttInput{Enabled: &on, NodeKind: "repeater"}); err != nil {
		t.Fatal(err)
	}
	if err := b.DeleteCompanion(t.Context(), c1); err != nil {
		t.Fatal(err)
	}
	if got := statusOf(b.SaveMqtt(t.Context(), api.MqttInput{Enabled: &on, NodeKind: "companion"})); got != http.StatusUnprocessableEntity {
		t.Errorf("companion feed with no companions: %d, want 422", got)
	}
}

// A config written whole (an import, or the channel save that rewrites the config) keeps the repeater as the feeding node.
func TestWriteConfig_KeepsTheRepeaterFeed(t *testing.T) {
	t.Parallel()
	b, _ := mqttBackend(t, true)
	cfg, err := loadConfigFromDB(t.Context(), b.db)
	if err != nil {
		t.Fatal(err)
	}
	kind := config.MqttNodeRepeater
	cfg.Mqtt.NodeKind = &kind
	if err := saveConfig(t.Context(), b.db, cfg); err != nil {
		t.Fatal(err)
	}
	m, err := b.db.Mqtt.Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if m.NodeKind != config.MqttNodeRepeater {
		t.Errorf("stored node kind = %q, want repeater", m.NodeKind)
	}
}
