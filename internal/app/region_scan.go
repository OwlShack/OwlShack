package app

import (
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/OwlShack/OwlShack/internal/api"
	"github.com/OwlShack/OwlShack/internal/client/repeater"
	"github.com/OwlShack/OwlShack/internal/discover"
)

// regionReqTimeout floors the wait on one repeater's regions reply; the route sizes it beyond that.
const regionReqTimeout = 5 * time.Second

// regionScanner outlives a radio generation so the page keeps the last run; a reload mid-run leaves the rest unanswered.
type regionScanner struct {
	mu          sync.Mutex
	state       api.RegionScanState
	listenUntil time.Time
}

func (s *regionScanner) snapshot() api.RegionScanState {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.state
	out.Repeaters = append([]api.RegionScanRepeater{}, s.state.Repeaters...)
	if out.Phase == "listening" {
		out.SecsLeft = max(0, int(time.Until(s.listenUntil).Seconds()))
	}
	return out
}

// start runs one scan in the background: discovery for repeaters, then each asked in turn, retried once, as the firmware rate-limits these requests.
func (s *regionScanner) start(disc *discover.Service, client *repeater.Client, name func(pubkeyHex string) string) (api.RegionScanState, error) {
	s.mu.Lock()
	if s.state.Running {
		s.mu.Unlock()
		return s.snapshot(), nil
	}
	if err := disc.Start(discover.FilterFor(discover.TypeRepeater), time.Time{}); err != nil {
		s.mu.Unlock()
		return api.RegionScanState{}, err
	}
	s.state = api.RegionScanState{Running: true, Phase: "listening", StartedAt: time.Now().UTC().Format(time.RFC3339), Repeaters: []api.RegionScanRepeater{}}
	s.listenUntil = time.Now().Add(discover.Window)
	s.mu.Unlock()

	go s.run(disc, client, name)
	return s.snapshot(), nil
}

func (s *regionScanner) run(disc *discover.Service, client *repeater.Client, name func(string) string) {
	time.Sleep(time.Until(s.listenUntil))
	_, _, results := disc.State()
	results = slices.DeleteFunc(results, func(r discover.Result) bool { return r.Type != discover.TypeRepeater })
	slices.SortFunc(results, func(a, b discover.Result) int { return int(b.SNR*4) - int(a.SNR*4) })

	s.mu.Lock()
	s.state.Phase = "asking"
	for _, r := range results {
		s.state.Repeaters = append(s.state.Repeaters, api.RegionScanRepeater{PubKey: r.PubKey, Name: name(r.PubKey), SNR: r.SNR, Status: "waiting", Regions: []string{}})
	}
	s.mu.Unlock()

	for i, r := range results {
		s.setStatus(i, "asking", nil)
		reply, err := client.RequestRegions(r.PubKey, regionReqTimeout)
		if err != nil { // one retry for a lost packet; a second would spend half the firmware's 4 per 3 minutes
			reply, err = client.RequestRegions(r.PubKey, regionReqTimeout)
		}
		if err != nil {
			s.setStatus(i, "no answer", nil)
			continue
		}
		s.setStatus(i, "answered", reply.Regions)
	}

	s.mu.Lock()
	s.state.Running, s.state.Phase = false, "done"
	s.mu.Unlock()
}

func (s *regionScanner) setStatus(i int, status string, regions []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Repeaters[i].Status = status
	if regions != nil {
		s.state.Repeaters[i].Regions = regions
	}
}

func (b *backend) StartRegionScan() (api.RegionScanState, error) {
	if b.discover == nil || len(b.companions) == 0 {
		return api.RegionScanState{}, errors.New("needs a running companion: it asks each repeater, and only a companion can")
	}
	return b.regionScan.start(b.discover, b.companions[0].Repeaters(), b.peerName)
}

func (b *backend) RegionScanState() api.RegionScanState {
	return b.regionScan.snapshot()
}
