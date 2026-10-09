package companion

import (
	"context"
	"time"

	"github.com/OwlShack/OwlShack/internal/config"
	"github.com/OwlShack/OwlShack/internal/node/advert"
)

func (c *Companion) advertLoop(ctx context.Context) {
	err := c.advert()
	if err != nil {
		c.log.Error("initial advert error", "error", err)
	}

	advertInterval := c.conf().AdvertInterval
	if advertInterval == nil || *advertInterval < 1 {
		oneDay := 86400
		advertInterval = &oneDay
	}

	interval := time.Duration(*advertInterval) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := c.advert()
			if err != nil {
				c.log.Error("advert error", "error", err)
			}
		}
	}
}

func (c *Companion) advert() error {
	return c.sendAdvert(true)
}

// SendAdvert broadcasts a self-advert; flood=false is the firmware's zero-hop advert, seen only by direct neighbours and never rebroadcast.
func (c *Companion) SendAdvert(flood bool) error {
	return c.sendAdvert(flood)
}

// sendAdvert reads one config snapshot, so a setting applied meanwhile cannot mix into the advert.
func (c *Companion) sendAdvert(flood bool) error {
	cfg := c.conf()
	lat, lon := advertLatLon(cfg)
	return advert.SendSelf(c.node, c.log, "CHAT", cfg.Name, lat, lon, flood, int(hashSizeOf(cfg)), cfg.FloodScope.MeshRegion())
}

// advertLatLon is the position an advert carries: none when the companion keeps it to itself.
func advertLatLon(cfg *config.CompanionConfig) (*float64, *float64) {
	if !cfg.SharesLocation() {
		return nil, nil
	}
	return cfg.Latitude, cfg.Longitude
}

// pathHashSize is the per-hop path hash width in bytes; startup resolves the global default into the block, so nil only happens in tests.
func (c *Companion) pathHashSize() uint8 { return hashSizeOf(c.conf()) }

func hashSizeOf(cfg *config.CompanionConfig) uint8 {
	if cfg.PathHashSize == nil {
		return config.DefaultPathHashSize
	}
	return uint8(*cfg.PathHashSize)
}
