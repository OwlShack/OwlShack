package companion

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/OwlShack/OwlShack/internal/config"
	"github.com/OwlShack/OwlShack/internal/trigger"
	meshcore "github.com/OwlShack/meshcore-go"
)

func identityFromHexSeed(seedHex string) (meshcore.LocalIdentity, error) {
	return config.LocalIdentityFromHex(seedHex)
}

func channelFromRef(ref config.ChannelRef) (*meshcore.ChannelEntry, error) {
	if ref.PrivateKey != "" {
		psk, err := hex.DecodeString(ref.PrivateKey)
		if err != nil {
			return nil, fmt.Errorf("invalid hex privateKey for channel %q: %w", ref.Name, err)
		}
		return meshcore.NewChannelFromPSK(ref.Name, psk)
	}
	if strings.EqualFold(ref.Name, "Public") {
		return meshcore.PublicChannel(), nil
	}
	nCh := meshcore.NormalizeHashtag(ref.Name)
	return meshcore.NewChannelFromHashtag(nCh), nil
}

func resolvePathHashSize(configured *uint8, evt trigger.Event, def uint8) uint8 {
	if configured == nil {
		return def
	}
	if *configured >= config.MinPathHashSize && *configured <= config.MaxPathHashSize {
		return *configured
	}
	if incoming, ok := evt.Data["PathHashSize"].(uint8); ok && incoming >= config.MinPathHashSize && incoming <= config.MaxPathHashSize {
		return incoming
	}
	return def
}
