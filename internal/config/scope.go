package config

import (
	"fmt"
	"strings"

	meshcore "github.com/OwlShack/meshcore-go"
)

// FloodScope is which region a flood we originate is scoped to: the level above's choice, unscoped, or one region.
type FloodScope string

const (
	ScopeInherit      FloodScope = "inherit"
	ScopeEverywhere   FloodScope = "everywhere"
	scopeRegionPrefix            = "region:"
)

func ScopeRegion(name string) FloodScope { return FloodScope(scopeRegionPrefix + name) }

// RegionName is the region's name, ok=false for inherit and everywhere.
func (s FloodScope) RegionName() (string, bool) {
	return strings.CutPrefix(string(s), scopeRegionPrefix)
}

// RequireScope is the API's check: the field must be sent, then Validate.
func RequireScope(s string, allowInherit bool) error {
	if s == "" {
		return fmt.Errorf("floodScope is required")
	}
	return FloodScope(s).Validate(allowInherit)
}

// NamesRegion is whether the scope is this region, "#" ignored as the firmware's findByNamePrefix does.
func (s FloodScope) NamesRegion(region string) bool {
	name, ok := s.RegionName()
	return ok && SameRegionName(region, name)
}

// MeshRegion is what a packet is scoped with; nil sends unscoped.
func (s FloodScope) MeshRegion() *meshcore.Region {
	name, ok := s.RegionName()
	if !ok {
		return nil
	}
	return meshcore.NewRegion(name)
}

// ResolveScope walks from the most specific level up and takes the first that does not inherit; unset inherits.
func ResolveScope(levels ...FloodScope) FloodScope {
	for _, s := range levels {
		if s != ScopeInherit && s != "" {
			return s
		}
	}
	return ScopeEverywhere
}

// Validate checks the value's form only; the API saves check the region is in the Settings list, and loading never does.
// Unset passes, as a config file means the default by it; the API requires the field before it gets here.
func (s FloodScope) Validate(allowInherit bool) error {
	switch s {
	case "", ScopeEverywhere:
		return nil
	case ScopeInherit:
		if allowInherit {
			return nil
		}
		return fmt.Errorf("there is no level above to inherit a region from")
	}
	name, ok := s.RegionName()
	if !ok {
		return fmt.Errorf("region scope %q must be inherit, everywhere or region:<name>", s)
	}
	return ValidateRegionName(name)
}

// ValidateRegionName takes the firmware's name characters, except "$" (a private region has no key to derive) and "#".
func ValidateRegionName(name string) error {
	if name == "" {
		return fmt.Errorf("region name is empty")
	}
	if len(name) > meshcore.MaxRegionName {
		return fmt.Errorf("region name %q is longer than %d bytes", name, meshcore.MaxRegionName)
	}
	for i := 0; i < len(name); i++ {
		if c := name[i]; c == '$' || c == '#' || !meshcore.IsValidRegionNameChar(c) {
			return fmt.Errorf("region name %q has a character the firmware does not take: %q", name, c)
		}
	}
	return nil
}

// ScopeForRegionName is the scope a repeater region name sends in: "#nz" keys as nz does, and a "$" region has no key here, so it sends unscoped.
func ScopeForRegionName(name string) FloodScope {
	bare := strings.TrimPrefix(name, "#")
	if bare == "" || bare == WildcardRegion || strings.HasPrefix(name, "$") {
		return ScopeEverywhere
	}
	return ScopeRegion(bare)
}

// scopeOrInherit fills a scope a config file left out.
func scopeOrInherit(s FloodScope) FloodScope {
	if s == "" {
		return ScopeInherit
	}
	return s
}

// MaxFloodRegions caps the Settings list: every received scoped flood is tried against each one. Twice a repeater's 32, for a mesh's worth of names.
const MaxFloodRegions = 64

// ScopeUnknown labels a scoped flood whose region is not one we know.
const ScopeUnknown = "unknown"

// FloodRegion is a name in the Settings region list; Parent is the region it sits under, "*" at the top, for organising only, as on a repeater.
type FloodRegion struct {
	Name   string `json:"name" yaml:"name" toml:"name"`
	Parent string `json:"parent,omitempty" yaml:"parent,omitempty" toml:"parent,omitempty"`
}

// LabelRegions are the regions a received flood is named from: the region list, then our repeater's relay regions ("#nz" as nz, "$" ones left out).
func (c *Config) LabelRegions() []*meshcore.Region {
	seen := make(map[string]bool)
	var out []*meshcore.Region
	add := func(name string) {
		name = strings.TrimPrefix(name, "#")
		if seen[name] || ValidateRegionName(name) != nil {
			return
		}
		seen[name] = true
		out = append(out, meshcore.NewRegion(name))
	}
	for _, rg := range c.FloodRegions {
		add(rg.Name)
	}
	if c.Repeater != nil {
		for _, rg := range c.Repeater.Regions {
			add(rg.Name)
		}
	}
	return out
}

// PacketScope names what a received packet carried: everywhere when it has no transport code, unknown when no region matches.
func PacketScope(pkt *meshcore.Packet, regions []*meshcore.Region) string {
	if !pkt.IsTransport() {
		return string(ScopeEverywhere)
	}
	for _, r := range regions {
		if r.MatchesPacket(pkt) {
			return string(ScopeRegion(r.Name))
		}
	}
	return ScopeUnknown
}
