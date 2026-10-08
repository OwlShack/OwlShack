package store

import (
	"slices"
	"testing"
)

// Before "#nz" and "nz" were one region, a list could hold both; the firmware only ever finds the first, so the second is dropped on read and what pointed at it points at the first.
func TestRepeaterGet_DropsHashDuplicate(t *testing.T) {
	st := newTestStore(t)
	exec(t, st.db, `INSERT INTO repeater (id, name, home_region, regions) VALUES (1, 'r', '#nz', ?)`,
		`[{"name":"*"},{"name":"nz","parent":"*"},{"name":"#nz","parent":"*","denyFlood":true},{"name":"akl","parent":"#nz"}]`)
	rep, err := st.Repeater.Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := []RepeaterRegion{{Name: "*"}, {Name: "nz", Parent: "*"}, {Name: "akl", Parent: "nz"}}
	if !slices.Equal(rep.Regions, want) || rep.HomeRegion != "nz" {
		t.Errorf("regions=%+v home=%q\nwant %+v home nz", rep.Regions, rep.HomeRegion, want)
	}
}

// A region under its own duplicate would sit under itself once merged, and a loop refuses the config at startup.
func TestRepeaterGet_DuplicateMergeBreaksLoops(t *testing.T) {
	st := newTestStore(t)
	exec(t, st.db, `INSERT INTO repeater (id, name, home_region, regions) VALUES (1, 'r', '*', ?)`,
		`[{"name":"*"},{"name":"nz","parent":"#nz"},{"name":"#nz","parent":"*"},{"name":"a","parent":"#b"},{"name":"b","parent":"#a"},{"name":"#a","parent":"*"},{"name":"#b","parent":"*"}]`)
	rep, err := st.Repeater.Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	parent := map[string]string{}
	for _, rg := range rep.Regions {
		parent[rg.Name] = rg.Parent
	}
	for _, rg := range rep.Regions {
		for p, steps := rg.Parent, 0; p != "" && p != "*"; p, steps = parent[p], steps+1 {
			if p == rg.Name || steps > len(rep.Regions) {
				t.Fatalf("%s sits inside itself: %+v", rg.Name, rep.Regions)
			}
		}
	}
}
