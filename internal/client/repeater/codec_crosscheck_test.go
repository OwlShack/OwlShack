package repeater

import (
	"encoding/hex"
	"testing"

	meshcore "github.com/OwlShack/meshcore-go"
)

// Our parsers are verified on air against firmware; the library's builders must produce what they read.
func TestLibraryCodecs_MatchOurParsers(t *testing.T) {
	st := meshcore.RepeaterStats{
		CommonStats: meshcore.CommonStats{
			BattMilliVolts: 4012, TxQueueLen: 2, NoiseFloor: -108, LastRSSI: -77,
			PacketsRecv: 1000, PacketsSent: 200, AirTimeSecs: 30, UpTimeSecs: 3600,
			SentFlood: 150, SentDirect: 50, RecvFlood: 900, RecvDirect: 100,
			ErrEvents: 1, LastSNR: 42, DirectDups: 3, FloodDups: 7,
		},
		RxAirTimeSecs: 90, RecvErrors: 4,
	}
	got, err := parseRepeaterStatus(meshcore.BuildRepeaterStats(st))
	if err != nil {
		t.Fatal(err)
	}
	if got.BatteryMV != 4012 || got.NoiseFloor != -108 || got.LastRSSI != -77 || got.UptimeSecs != 3600 ||
		got.FloodRx != 900 || got.DirectTx != 50 || got.LastSNR != 10.5 || got.FloodDups != 7 ||
		got.RxAirSecs != 90 || got.RecvErrors != 4 {
		t.Errorf("status = %+v", got)
	}
	// And the other way: what our parser reads, the library reads the same.
	back, err := meshcore.ParseRepeaterStats(meshcore.BuildRepeaterStats(st))
	if err != nil || back != st {
		t.Errorf("library round trip = %+v, %v", back, err)
	}

	nb := meshcore.BuildNeighboursReply(meshcore.NeighboursReply{Total: 5, Neighbours: []meshcore.Neighbour{
		{Prefix: []byte{0xAA, 0xBB, 0xCC, 0xDD}, HeardSecsAgo: 120, SNR: -14},
	}})
	ns, err := parseRepeaterNeighbors(nb, 4)
	if err != nil {
		t.Fatal(err)
	}
	if ns.TotalCount != 5 || len(ns.Neighbors) != 1 || ns.Neighbors[0].PubkeyPrefix != "aabbccdd" ||
		ns.Neighbors[0].SecsAgo != 120 || ns.Neighbors[0].SNR != -3.5 {
		t.Errorf("neighbours = %+v", ns)
	}

	prefix, _ := hex.DecodeString("010203040506")
	acl := parseRepeaterAccessList(meshcore.BuildAccessListReply([]meshcore.ACLEntry{{Prefix: [6]byte(prefix), Permissions: 3}}))
	if len(acl.Entries) != 1 || acl.Entries[0].PubkeyPrefix != "010203040506" || acl.Entries[0].Permissions != 3 {
		t.Errorf("access list = %+v", acl)
	}

	oi := parseRepeaterOwnerInfo(meshcore.BuildOwnerInfoReply(meshcore.OwnerInfo{FirmwareVersion: "v1.17.1", Name: "Owly", Owner: "Wes\nAkl"}))
	if oi.FirmwareVersion != "v1.17.1" || oi.NodeName != "Owly" || oi.OwnerInfo != "Wes\nAkl" {
		t.Errorf("owner info = %+v", oi)
	}
}
