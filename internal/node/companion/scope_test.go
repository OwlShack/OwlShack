package companion

import (
	"encoding/hex"
	"testing"

	meshcore "github.com/OwlShack/meshcore-go"
	"github.com/OwlShack/meshcore-go/node"

	"github.com/OwlShack/OwlShack/internal/client/repeater"
	"github.com/OwlShack/OwlShack/internal/config"
)

var testRegions = []*meshcore.Region{meshcore.NewRegion("nz"), meshcore.NewRegion("wlg"), meshcore.NewRegion("akl")}

// scopeCompanion is hashSizeCompanion in region nz, with its contact friend overridden to wlg.
func scopeCompanion(t *testing.T) (*Companion, *recordingRadio, meshcore.LocalIdentity, meshcore.LocalIdentity) {
	t.Helper()
	c, radio, self, friend := hashSizeCompanion(t)
	c.cfg.FloodScope = "region:nz"
	c.chanScopes = map[string]config.FloodScope{}
	var err error
	c.store.WriteSync(func() {
		err = c.store.Contacts.SetFloodScope(c.runCtx, c.cfg.ID, friend.PublicKeyBytes(), "region:wlg")
	})
	if err != nil {
		t.Fatal(err)
	}
	c.repeaters = repeater.NewClient(c.node, c.store, c.cfg.ID, c.log, nil, c.pathHashSize,
		func(pubkey []byte) *meshcore.Region { return c.contactScope(pubkey).MeshRegion() })
	return c, radio, self, friend
}

// sentScope labels every packet the radio sent, failing on any that went direct, which would prove nothing.
func sentScope(t *testing.T, radio *recordingRadio) string {
	t.Helper()
	sent := radio.take()
	if len(sent) == 0 {
		t.Fatal("nothing was sent")
	}
	label := ""
	for i, raw := range sent {
		pkt, err := meshcore.PacketFromBytes(raw)
		if err != nil {
			t.Fatal(err)
		}
		if !pkt.IsRouteFlood() {
			t.Fatalf("packet %d went direct, so this proves nothing about a flood's region", i)
		}
		got := config.PacketScope(pkt, testRegions)
		if i > 0 && got != label {
			t.Fatalf("packets of one send disagree: %q then %q", label, got)
		}
		label = got
	}
	return label
}

func TestScope_ContactOverrideAndCompanionFallback(t *testing.T) {
	c, radio, self, friend := scopeCompanion(t)
	friendSecret, _ := friend.SharedSecret(self.Identity)
	stranger := meshcore.NewLocalIdentityFromSeed([32]byte{9})
	strangerSecret, _ := stranger.SharedSecret(self.Identity)
	direct := &meshcore.Packet{Header: meshcore.MakeHeader(meshcore.RouteTypeDirect, meshcore.PayloadTypeTxtMsg, 0)}
	flooded := &meshcore.Packet{Header: meshcore.MakeHeader(meshcore.RouteTypeFlood, meshcore.PayloadTypeTxtMsg, 0), PathLength: 1, Path: []byte{0xAA}}

	if err := c.SendContactMessage(hex.EncodeToString(friend.PublicKeyBytes()), "hi"); err != nil {
		t.Fatal(err)
	}
	if got := sentScope(t, radio); got != "region:wlg" {
		t.Errorf("DM to the contact went in %q, want its own wlg", got)
	}
	c.sendDMAck(direct, friend.PublicKeyBytes(), friendSecret, []byte{1, 2, 3, 4})
	if got := sentScope(t, radio); got != "region:wlg" {
		t.Errorf("ACK to the contact went in %q, want wlg", got)
	}
	c.sendDMAck(flooded, friend.PublicKeyBytes(), friendSecret, []byte{1, 2, 3, 4})
	if got := sentScope(t, radio); got != "region:wlg" {
		t.Errorf("path return to the contact went in %q, want wlg", got)
	}
	c.sendDMAck(direct, stranger.PublicKeyBytes(), strangerSecret, []byte{1, 2, 3, 4})
	if got := sentScope(t, radio); got != "region:nz" {
		t.Errorf("ACK to a stranger went in %q, want the companion's nz", got)
	}
	c.node.Peers().Insert(&node.Peer{Identity: friend.Identity})
	if _, err := c.repeaters.SendLogin(hex.EncodeToString(friend.PublicKeyBytes()), "pw", 1); err == nil {
		t.Fatal("login answered with nothing listening")
	}
	if got := sentScope(t, radio); got != "region:wlg" {
		t.Errorf("login to the contact went in %q, want wlg", got)
	}
}

func TestScope_InheritFollowsTheCompanion(t *testing.T) {
	c, radio, _, friend := scopeCompanion(t)
	var err error
	c.store.WriteSync(func() {
		err = c.store.Contacts.SetFloodScope(c.runCtx, c.cfg.ID, friend.PublicKeyBytes(), "inherit")
	})
	if err != nil {
		t.Fatal(err)
	}
	c.cfg.FloodScope = "region:akl"
	if err := c.SendContactMessage(hex.EncodeToString(friend.PublicKeyBytes()), "hi"); err != nil {
		t.Fatal(err)
	}
	if got := sentScope(t, radio); got != "region:akl" {
		t.Errorf("an inheriting contact's DM went in %q, want the companion's akl", got)
	}
	c.cfg.FloodScope = config.ScopeEverywhere
	if err := c.sendAdvert(true); err != nil {
		t.Fatal(err)
	}
	if got := sentScope(t, radio); got != "everywhere" {
		t.Errorf("an everywhere companion's advert went %q, want unscoped", got)
	}
}

func TestScope_ChannelAndBotOverrides(t *testing.T) {
	c, radio, _, _ := scopeCompanion(t)
	pub, err := channelFromRef(config.ChannelRef{Name: "Public"})
	if err != nil {
		t.Fatal(err)
	}
	test, err := channelFromRef(config.ChannelRef{Name: "#westest"})
	if err != nil {
		t.Fatal(err)
	}
	c.node.SetChannel(0, pub)
	c.node.SetChannel(1, test)
	c.chanScopes[test.Name] = config.ScopeEverywhere

	if err := c.SendChannelMessage(pub.Name, "hi"); err != nil {
		t.Fatal(err)
	}
	if got := sentScope(t, radio); got != "region:nz" {
		t.Errorf("an inheriting channel posted in %q, want the companion's nz", got)
	}
	if err := c.SendChannelMessage(test.Name, "hi"); err != nil {
		t.Fatal(err)
	}
	if got := sentScope(t, radio); got != "everywhere" {
		t.Errorf("a channel set to everywhere posted %q, want unscoped", got)
	}
	bot := config.ResolveScope("region:akl", c.channelScope(test.Name))
	if err := c.sendGroupReply(test, "bot", 1, 0, 0, bot); err != nil {
		t.Fatal(err)
	}
	if got := sentScope(t, radio); got != "region:akl" {
		t.Errorf("a bot with its own region posted in %q, want akl", got)
	}
}

// Runtime channel edits are persisted from StandaloneChannels, so a rename that dropped the override would reset it on save.
func TestScope_ChannelOverrideSurvivesARename(t *testing.T) {
	c, _, _, _ := scopeCompanion(t)
	if err := c.AddChannel(config.ChannelRef{Name: "Public", FloodScope: "region:akl"}); err != nil {
		t.Fatal(err)
	}
	if err := c.RenameChannel("Public", "General"); err != nil {
		t.Fatal(err)
	}
	for _, ref := range c.StandaloneChannels() {
		if ref.Name == "General" {
			if ref.FloodScope != "region:akl" {
				t.Errorf("renamed channel persists as %q, want region:akl", ref.FloodScope)
			}
			return
		}
	}
	t.Fatal("renamed channel missing")
}

// The rename dialog saves even when nothing changed, and renaming onto another channel would take over its scope.
func TestScope_RenameToSameOrTakenName(t *testing.T) {
	c, _, _, _ := scopeCompanion(t)
	for _, ref := range []config.ChannelRef{{Name: "Public", FloodScope: "region:akl"}, {Name: "#other", FloodScope: "region:nz"}} {
		if err := c.AddChannel(ref); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.RenameChannel("Public", "Public"); err != nil {
		t.Fatalf("same-name rename: %v", err)
	}
	if err := c.RenameChannel("Public", "#other"); err == nil {
		t.Error("renaming onto an existing channel was accepted")
	}
	got := map[string]config.FloodScope{}
	for _, ref := range c.StandaloneChannels() {
		got[ref.Name] = ref.FloodScope
	}
	if got["Public"] != "region:akl" || got["#other"] != "region:nz" {
		t.Errorf("scopes after renames = %v, want both kept", got)
	}
}
