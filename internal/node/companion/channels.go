package companion

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"

	meshcore "github.com/OwlShack/meshcore-go"
	"github.com/OwlShack/meshcore-go/node"

	"github.com/OwlShack/OwlShack/internal/api"
	"github.com/OwlShack/OwlShack/internal/config"
)

func (c *Companion) AddChannel(ref config.ChannelRef) error {
	if err := config.CheckChannelName(ref.Name); err != nil {
		return api.Invalid(err)
	}
	if err := ref.Validate(); err != nil {
		return api.Invalid(err)
	}
	ch, err := channelFromRef(ref)
	if err != nil {
		return fmt.Errorf("invalid channel %q: %w", ref.Name, err)
	}

	for i := range node.DefaultMaxChannels {
		if existing := c.node.Channel(i); existing != nil && existing.Name == ch.Name {
			return fmt.Errorf("channel %q already exists", ch.Name)
		}
	}

	idx := c.nextFreeChannelIndex()
	if idx < 0 {
		return fmt.Errorf("no free channel slots")
	}

	if !c.node.SetChannel(idx, ch) {
		return fmt.Errorf("failed to set channel at index %d", idx)
	}
	c.chanScopesMu.Lock()
	c.chanScopes[ch.Name] = ref.FloodScope
	c.chanScopesMu.Unlock()

	c.log.Info("channel added", "channel", ch.Name, "index", idx)
	return nil
}

func (c *Companion) RemoveChannel(name string) error {
	if used := c.channelTriggerUsage(name); used != "" {
		return fmt.Errorf("channel %q is in use by the %s; remove that trigger usage first", name, used)
	}
	for i := range node.DefaultMaxChannels {
		ch := c.node.Channel(i)
		if ch != nil && ch.Name == name {
			c.node.RemoveChannel(i)
			c.chanScopesMu.Lock()
			delete(c.chanScopes, name)
			c.chanScopesMu.Unlock()
			c.log.Info("channel removed", "channel", name, "index", i)
			return nil
		}
	}
	return api.Failed(http.StatusNotFound, fmt.Errorf("channel %q not found", name))
}

func (c *Companion) RenameChannel(oldName, newName string) error {
	if oldName == newName {
		return nil // a save with nothing changed; the scope swap below would delete the scope
	}
	if err := config.CheckChannelName(newName); err != nil {
		return api.Invalid(err)
	}
	if ch := c.findChannel(oldName); ch != nil && !bytes.Equal(ch.PSK, meshcore.PublicChannel().PSK) {
		return api.Invalid(fmt.Errorf("only the Public channel can be renamed; %q would keep its old key under the new name", oldName))
	}
	if used := c.channelTriggerUsage(oldName); used != "" {
		return fmt.Errorf("channel %q is in use by the %s; remove that trigger usage first", oldName, used)
	}
	for i := range node.DefaultMaxChannels {
		if ch := c.node.Channel(i); ch != nil && ch.Name == newName {
			return fmt.Errorf("a channel named %q already exists", newName)
		}
	}
	for i := range node.DefaultMaxChannels {
		ch := c.node.Channel(i)
		if ch != nil && ch.Name == oldName {
			ch.Name = newName
			c.chanScopesMu.Lock()
			c.chanScopes[newName] = c.chanScopes[oldName]
			delete(c.chanScopes, oldName)
			c.chanScopesMu.Unlock()
			c.log.Info("channel renamed", "old", oldName, "new", newName, "index", i)
			return nil
		}
	}
	return api.Failed(http.StatusNotFound, fmt.Errorf("channel %q not found", oldName))
}

// channelTriggerUsage describes the first trigger referencing the named channel, or "" if none do; matching is case-sensitive, as elsewhere in the channel code.
func (c *Companion) channelTriggerUsage(name string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.triggers {
		for _, ch := range e.channels {
			if ch.Name == name {
				if e.config.Type == "cron" {
					return "cron bot"
				}
				return "group bot"
			}
		}
	}
	return ""
}

func (c *Companion) nextFreeChannelIndex() int {
	for i := 0; i < node.DefaultMaxChannels; i++ {
		if c.node.Channel(i) == nil {
			return i
		}
	}
	return -1
}

// StandaloneChannels returns every channel registered on the node for config persistence; hashtag/Public channels omit their derived key.
func (c *Companion) StandaloneChannels() []config.ChannelRef {
	allChs := c.node.Channels()
	c.chanScopesMu.Lock()
	defer c.chanScopesMu.Unlock()
	var refs []config.ChannelRef
	for _, ch := range allChs {
		if ch == nil {
			continue
		}
		ref := config.ChannelRef{Name: ch.Name, FloodScope: c.chanScopes[ch.Name]}
		if !isHashtagChannel(ch) {
			ref.PrivateKey = hex.EncodeToString(ch.PSK[:])
		}
		refs = append(refs, ref)
	}
	return refs
}

func isHashtagChannel(ch *meshcore.ChannelEntry) bool {
	if strings.HasPrefix(ch.Name, "#") {
		derived := meshcore.NewChannelFromHashtag(meshcore.NormalizeHashtag(ch.Name))
		return bytes.Equal(derived.PSK, ch.PSK)
	}
	if strings.EqualFold(ch.Name, "Public") {
		return bytes.Equal(meshcore.PublicChannel().PSK, ch.PSK)
	}
	return false
}
