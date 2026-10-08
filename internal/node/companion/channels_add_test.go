package companion

import (
	"errors"
	"strings"
	"testing"

	"github.com/OwlShack/OwlShack/internal/api"
	"github.com/OwlShack/OwlShack/internal/config"
)

// A key the stored config would refuse at the next start must be refused here, or OwlShack saves it and won't boot.
func TestAddChannel_RefusesWhatConfigRefuses(t *testing.T) {
	c, _, _, _ := scopeCompanion(t)
	var verr *api.ValidationError
	if err := c.AddChannel(config.ChannelRef{Name: "Long", PrivateKey: strings.Repeat("ab", 32)}); !errors.As(err, &verr) {
		t.Fatalf("32-byte key: err = %v, want a validation error", err)
	}
	if c.findChannel("Long") != nil {
		t.Error("the refused channel was added")
	}
	if err := c.AddChannel(config.ChannelRef{Name: "Ok", PrivateKey: strings.Repeat("ab", 16)}); err != nil {
		t.Fatalf("16-byte key: %v", err)
	}
}

// A blank or space-padded name is refused rather than becoming a hashtag channel of spaces.
func TestAddChannel_RefusesABlankOrPaddedName(t *testing.T) {
	c, _, _, _ := scopeCompanion(t)
	for _, name := range []string{"   ", " #padded "} {
		var verr *api.ValidationError
		if err := c.AddChannel(config.ChannelRef{Name: name}); !errors.As(err, &verr) {
			t.Errorf("name %q: err = %v, want a validation error", name, err)
		}
	}
}

// Only Public is renamed, as the thread menu offers; a hashtag channel's name is its key.
func TestRenameChannel_OnlyPublic(t *testing.T) {
	c, _, _, _ := scopeCompanion(t)
	for i, ref := range []config.ChannelRef{{Name: "Public"}, {Name: "#jokes"}} {
		ch, err := channelFromRef(ref)
		if err != nil {
			t.Fatal(err)
		}
		c.node.SetChannel(i, ch)
	}
	var verr *api.ValidationError
	if err := c.RenameChannel("#jokes", "#other"); !errors.As(err, &verr) {
		t.Errorf("renaming a hashtag channel: err = %v, want a validation error", err)
	}
	if err := c.RenameChannel("Public", "  "); !errors.As(err, &verr) {
		t.Errorf("renaming to a blank name: err = %v, want a validation error", err)
	}
	if err := c.RenameChannel("Public", "General"); err != nil {
		t.Fatalf("renaming Public: %v", err)
	}
	if err := c.RenameChannel("General", "Public"); err != nil {
		t.Errorf("renaming the renamed Public back: %v", err)
	}
}

// "#test256" shares Public's one-byte channel hash (0x11), so only the whole key tells them apart.
func TestChannels_PublicIsKnownByItsKey(t *testing.T) {
	c, _, _, _ := scopeCompanion(t)
	for i, ref := range []config.ChannelRef{{Name: "Public"}, {Name: "#test256"}} {
		ch, err := channelFromRef(ref)
		if err != nil {
			t.Fatal(err)
		}
		c.node.SetChannel(i, ch)
	}
	var verr *api.ValidationError
	if err := c.RenameChannel("#test256", "General"); !errors.As(err, &verr) {
		t.Errorf("renaming #test256: err = %v, want a validation error", err)
	}
	c.node.SetChannel(1, nil)
	if err := c.RenameChannel("Public", "#test256"); err != nil {
		t.Fatal(err)
	}
	for _, ref := range c.StandaloneChannels() {
		if ref.Name == "#test256" && ref.PrivateKey == "" {
			t.Error("Public renamed #test256 is saved without its key, so it comes back as a different channel")
		}
	}
}
