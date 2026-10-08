package repeater

import (
	"context"
	"time"

	meshcore "github.com/OwlShack/meshcore-go"

	"github.com/OwlShack/OwlShack/internal/node/advert"
)

// Firmware setup() holds the boot advert ~16s to let the radio settle.
const bootAdvertDelay = 16 * time.Second

// advertLoop runs the firmware's two schedules, zero-hop and flood; either interval of 0 disables that one.
func (r *Repeater) advertLoop(ctx context.Context) {
	localSecs := r.cfg.AdvertIntervalOr()
	floodSecs := r.cfg.FloodAdvertIntervalOr()

	boot := time.NewTimer(bootAdvertDelay)
	defer boot.Stop()

	// A nil channel blocks forever in select, disabling a schedule whose interval is 0.
	var localC, floodC <-chan time.Time
	if localSecs > 0 {
		t := time.NewTicker(time.Duration(localSecs) * time.Second)
		defer t.Stop()
		localC = t.C
	}
	if floodSecs > 0 {
		t := time.NewTicker(time.Duration(floodSecs) * time.Second)
		defer t.Stop()
		floodC = t.C
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-boot.C:
			if err := r.sendAdvert(false); err != nil { // zero-hop, matching firmware boot
				r.log.Error("initial advert error", "error", err)
			}
		case <-localC:
			if err := r.sendAdvert(false); err != nil {
				r.log.Error("zero-hop advert error", "error", err)
			}
		case <-floodC:
			if err := r.sendAdvert(true); err != nil {
				r.log.Error("flood advert error", "error", err)
			}
		}
	}
}

// SendAdvert broadcasts a self-advert on demand; flood=false reaches direct neighbours only.
func (r *Repeater) SendAdvert(flood bool) error { return r.sendAdvert(flood) }

// sendAdvert emits a signed REPEATER advert, scoped through the configured default region when one is set.
func (r *Repeater) sendAdvert(flood bool) error {
	err := advert.SendSelf(r.node, r.log, "REPEATER", r.cfg.Name,
		r.cfg.Latitude, r.cfg.Longitude, flood, r.cfg.PathHashSizeOr(), r.defaultRegionScope())
	if err == nil {
		r.countTx(flood)
	}
	return err
}

// defaultRegionScope is the region for our own floods; it need not be one we relay, so it is not the library's map default.
func (r *Repeater) defaultRegionScope() *meshcore.Region {
	return r.cfgSnapshot().FloodScope.MeshRegion()
}

// replyScope is the firmware's chooseReplyScope with our own region as the fallback: the request's region, none for an allowed unscoped flood.
func (r *Repeater) replyScope(req *meshcore.Packet) *meshcore.Region {
	rm := r.node.Regions()
	if req.IsRouteFlood() {
		if m := rm.FindFloodMatch(req); m != nil {
			if rm.IsWildcard(m) {
				return nil
			}
			return m
		}
	}
	return r.defaultRegionScope()
}
