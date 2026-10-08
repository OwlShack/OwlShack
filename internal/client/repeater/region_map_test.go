package repeater

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// fakeRegionCLI answers like CommonCLI::handleRegionCmd for a map given as name -> parent ("" for "*"), in firmware slot order.
func fakeRegionCLI(t *testing.T, names, parents []string, denied map[string]bool, home, def string, override ...map[string]string) (func(string) (string, error), *[]string) {
	t.Helper()
	var asked []string
	flag := func(n string) string {
		if denied[n] {
			return ""
		}
		return " F"
	}
	var tree strings.Builder
	var walk func(parent string, depth int)
	walk = func(parent string, depth int) {
		for i, n := range names {
			if parents[i] != parent {
				continue
			}
			mark := ""
			if n == home {
				mark = "^"
			}
			fmt.Fprintf(&tree, "%s%s%s%s\n", strings.Repeat(" ", depth), strings.TrimPrefix(n, "#"), mark, flag(n))
			walk(n, depth+1)
		}
	}
	mark := ""
	if home == "" {
		mark = "^"
	}
	fmt.Fprintf(&tree, "*%s%s\n", mark, flag("*"))
	walk("*", 1)
	full := tree.String()
	list := func(deny bool) string {
		var out []string
		if denied["*"] == deny {
			out = append(out, "*")
		}
		for _, n := range names {
			written := len(strings.Join(out, ","))
			if len(out) > 0 {
				written++ // the trailing comma exportNamesTo has already written
			}
			if denied[n] == deny && written+len(n)+2 < 160 {
				out = append(out, strings.TrimPrefix(n, "#"))
			}
		}
		if len(out) == 0 {
			return "-none-"
		}
		return strings.Join(out, ",")
	}
	return func(cmd string) (string, error) {
		asked = append(asked, cmd)
		for _, o := range override {
			if r, ok := o[cmd]; ok {
				if r == "LOST" {
					return "", fmt.Errorf("CLI command timed out after 10s: %w", ErrNoReply)
				}
				return r, nil
			}
		}
		switch {
		case cmd == "region":
			return full[:min(len(full), cliReplyMax)], nil
		case cmd == "region list allowed":
			return list(false), nil
		case cmd == "region list denied":
			return list(true), nil
		case cmd == "region home":
			if home == "" {
				return " home is *", nil
			}
			return " home is " + home, nil
		case cmd == "region default":
			if def == "" {
				return " default scope is <null>", nil
			}
			return " default scope is " + def, nil
		case strings.HasPrefix(cmd, "region get "):
			i := slices.IndexFunc(names, func(n string) bool { return strings.TrimPrefix(n, "#") == cmd[len("region get "):] })
			if i < 0 {
				return "Err - unknown region", nil
			}
			if parents[i] == "*" {
				return fmt.Sprintf(" %s %s", names[i], strings.TrimSpace(flag(names[i]))), nil
			}
			return fmt.Sprintf(" %s (%s) %s", names[i], parents[i], strings.TrimSpace(flag(names[i]))), nil
		}
		return "Err - ??", nil
	}, &asked
}

func TestReadRegions_WholeTree(t *testing.T) {
	cli, asked := fakeRegionCLI(t,
		[]string{"nz", "#akl", "au"}, []string{"*", "nz", "*"},
		map[string]bool{"#akl": true}, "nz", "#akl")
	m, err := readRegions(context.Background(), cli)
	if err != nil {
		t.Fatal(err)
	}
	want := []RegionEntry{{"*", "", false}, {"nz", "*", false}, {"akl", "nz", true}, {"au", "*", false}}
	if !slices.Equal(m.Regions, want) {
		t.Errorf("regions = %+v\nwant      %+v", m.Regions, want)
	}
	if m.Home != "nz" || !m.DefaultSupported || m.Default != "akl" || m.Partial {
		t.Errorf("home=%q default=%q/%v partial=%v, want nz, akl, false", m.Home, m.Default, m.DefaultSupported, m.Partial)
	}
	if want := []string{"region", "region home", "region default"}; !slices.Equal(*asked, want) {
		t.Errorf("asked %q, want only %q", *asked, want)
	}
}

// A tree longer than the reply is cut at 159 bytes; the rest comes from the name lists and `region get`.
func TestReadRegions_CutTree(t *testing.T) {
	names := []string{"nz"}
	parents := []string{"*"}
	for i := range 14 {
		names = append(names, fmt.Sprintf("akl-%02d", i))
		parents = append(parents, "nz")
	}
	names = append(names, "#sub")
	parents = append(parents, "akl-13")
	cli, asked := fakeRegionCLI(t, names, parents, map[string]bool{"*": true, "akl-12": true}, "akl-13", "")
	m, err := readRegions(context.Background(), cli)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]RegionEntry{}
	for _, r := range m.Regions {
		got[r.Name] = r
	}
	if len(got) != len(names)+1 || len(m.Regions) != len(got) {
		t.Fatalf("read %d regions (%d distinct), want %d: %+v", len(m.Regions), len(got), len(names)+1, m.Regions)
	}
	for i, n := range names {
		n = strings.TrimPrefix(n, "#")
		if r := got[n]; r.Parent != parents[i] || r.DenyFlood != (n == "akl-12") {
			t.Errorf("%s = %+v, want parent %s", n, r, parents[i])
		}
	}
	if !got["*"].DenyFlood {
		t.Error(`"*" lost its deny flood`)
	}
	if m.Home != "akl-13" || !m.DefaultSupported || m.Default != "" || m.Partial {
		t.Errorf("home=%q default=%q partial=%v, want akl-13, none and whole", m.Home, m.Default, m.Partial)
	}
	if !slices.Contains(*asked, "region home") || !slices.Contains(*asked, "region get sub") {
		t.Errorf("asked %q, want the home and the cut regions asked for", *asked)
	}
}

// Names the list reply had no room for are skipped by the firmware, so the map says it may be missing some.
func TestReadRegions_ListsCutToo(t *testing.T) {
	var names, parents []string
	for i := range 16 {
		names = append(names, fmt.Sprintf("auckland-%02d", i))
		parents = append(parents, "*")
	}
	cli, _ := fakeRegionCLI(t, names, parents, nil, "", "")
	m, err := readRegions(context.Background(), cli)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Partial || len(m.Regions) > len(names) {
		t.Errorf("partial=%v with %d regions, want partial and fewer than %d", m.Partial, len(m.Regions), len(names)+1)
	}
}

func TestReadRegions_Unsupported(t *testing.T) {
	_, err := readRegions(context.Background(), func(string) (string, error) { return "Err - ??", nil })
	if !errors.Is(err, ErrNoRegions) {
		t.Errorf("err = %v, want ErrNoRegions", err)
	}
	m, err := readRegions(context.Background(), func(cmd string) (string, error) {
		switch cmd {
		case "region":
			return "*^ F\n", nil
		case "region home":
			return " home is *", nil
		}
		return "Err - ??", nil
	})
	if err != nil || m.DefaultSupported {
		t.Errorf("no region default: m=%+v err=%v, want it unsupported", m, err)
	}
}

// exportNamesTo stops once a 30-byte name can't fit, which can leave a reply of exactly 127 bytes.
func TestReadRegions_ListCutAtTheBoundary(t *testing.T) {
	var names, parents []string
	for i := range 8 {
		names = append(names, fmt.Sprintf("region-%06d", i)) // 13 bytes
		parents = append(parents, "*")
	}
	names = append(names, "region-long-000", "x"+strings.Repeat("y", 29)) // 15 bytes brings the list to 127; 30 bytes no longer fits
	parents = append(parents, "*", "*")
	denied := map[string]bool{}
	for _, n := range names {
		denied[n] = true
	}
	cli, _ := fakeRegionCLI(t, names, parents, denied, "", "")
	if got, _ := cli("region list denied"); len(got) != 127 {
		t.Fatalf("set-up: denied list is %d bytes, want 127", len(got))
	}
	m, err := readRegions(context.Background(), cli)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Partial {
		t.Error("a 127-byte list that skipped a name was read as whole")
	}
}

// A lost `region get` leaves that region out and says so, keeping everything else read.
func TestReadRegions_LostGetIsPartial(t *testing.T) {
	names := []string{"nz"}
	parents := []string{"*"}
	for i := range 14 {
		names = append(names, fmt.Sprintf("akl-%02d", i))
		parents = append(parents, "nz")
	}
	cli, _ := fakeRegionCLI(t, names, parents, nil, "", "", map[string]string{"region get akl-13": "LOST"})
	m, err := readRegions(context.Background(), cli)
	if err != nil {
		t.Fatalf("one lost reply failed the read: %v", err)
	}
	if !m.Partial || slices.ContainsFunc(m.Regions, func(r RegionEntry) bool { return r.Name == "akl-13" }) || len(m.Regions) != len(names) {
		t.Errorf("partial=%v regions=%d, want partial without akl-13 and the rest kept", m.Partial, len(m.Regions))
	}
}

// A `region home` reply that isn't one is an error, never a home region.
func TestReadRegions_BadHomeReply(t *testing.T) {
	names := []string{"nz"}
	parents := []string{"*"}
	for i := range 14 {
		names = append(names, fmt.Sprintf("akl-%02d", i))
		parents = append(parents, "nz")
	}
	cli, _ := fakeRegionCLI(t, names, parents, nil, "akl-13", "", map[string]string{"region home": "Err - ??"})
	if m, err := readRegions(context.Background(), cli); err == nil {
		t.Errorf("home read as %q, want an error", m.Home)
	}
}

// A request the browser left sends nothing more.
func TestReadRegions_CancelledSendsNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cli, asked := fakeRegionCLI(t, []string{"nz"}, []string{"*"}, nil, "", "")
	if _, err := readRegions(ctx, cli); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if len(*asked) != 0 {
		t.Errorf("sent %q after the request was cancelled", *asked)
	}
}

// The tree prints "^" right after a home region's name, and a name may end in "^" too, so the home comes from `region home`.
func TestReadRegions_CaretInNames(t *testing.T) {
	for _, c := range []struct {
		desc, home  string
		override    map[string]string
		wantHome    string
		wantRegions []string
	}{
		{"a^ beside home nz", "nz", nil, "nz", []string{"*", "nz", "a^"}},
		{"a^ is home", "a^", nil, "a^", []string{"*", "nz", "a^"}},
		{"home removed, so no line is marked", "ghost", map[string]string{"region home": " home is *"}, "*", []string{"*", "nz", "a^"}},
	} {
		t.Run(c.desc, func(t *testing.T) {
			cli, _ := fakeRegionCLI(t, []string{"nz", "a^"}, []string{"*", "nz"}, nil, c.home, "", c.override)
			m, err := readRegions(context.Background(), cli)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, r := range m.Regions {
				got = append(got, r.Name)
			}
			if !slices.Equal(got, c.wantRegions) || m.Home != c.wantHome {
				t.Errorf("regions=%q home=%q, want %q and %q", got, m.Home, c.wantRegions, c.wantHome)
			}
		})
	}
}

// Home region a and region a^ both print "a^"; then only `region get` can say which line is which.
func TestReadRegions_CaretAmbiguous(t *testing.T) {
	cli, asked := fakeRegionCLI(t, []string{"a^", "a", "kid"}, []string{"*", "*", "a^"}, map[string]bool{"a": true}, "a", "")
	m, err := readRegions(context.Background(), cli)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]RegionEntry{
		"*":   {"*", "", false},
		"a":   {"a", "*", true},
		"a^":  {"a^", "*", false},
		"kid": {"kid", "a^", false},
	}
	if len(m.Regions) != len(want) {
		t.Fatalf("regions = %+v, want %d", m.Regions, len(want))
	}
	for _, r := range m.Regions {
		if want[r.Name] != r {
			t.Errorf("%s = %+v, want %+v", r.Name, r, want[r.Name])
		}
	}
	if m.Home != "a" || !slices.Contains(*asked, "region get a^") {
		t.Errorf("home=%q asked=%q, want a and the regions asked for one by one", m.Home, *asked)
	}
}

// Only a lost reply makes the map partial; a login that's gone stops the read, so the page can ask for it again.
func TestReadRegions_LostLoginIsAnError(t *testing.T) {
	names := []string{"nz"}
	parents := []string{"*"}
	for i := range 14 {
		names = append(names, fmt.Sprintf("akl-%02d", i))
		parents = append(parents, "nz")
	}
	cli, _ := fakeRegionCLI(t, names, parents, nil, "", "")
	lost := func(cmd string) (string, error) {
		if strings.HasPrefix(cmd, "region get ") {
			return "", fmt.Errorf("%w to this repeater", ErrNotLoggedIn)
		}
		return cli(cmd)
	}
	if _, err := readRegions(context.Background(), lost); !errors.Is(err, ErrNotLoggedIn) {
		t.Errorf("err = %v, want not logged in", err)
	}
}

// The tree prints names without "#" from firmware v1.12 (earlier firmware has no remote tree), but home, get and default print the stored name, "#" included.
func TestReadRegions_HashInReplies(t *testing.T) {
	m, err := readRegions(context.Background(), func(cmd string) (string, error) {
		switch cmd {
		case "region":
			return "* F\n nz^ F\n  akl\n", nil
		case "region home":
			return " home is #nz", nil
		}
		return " default scope is #nz", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []RegionEntry{{"*", "", false}, {"nz", "*", false}, {"akl", "nz", true}}
	if !slices.Equal(m.Regions, want) || m.Home != "nz" || m.Default != "nz" {
		t.Errorf("regions=%+v home=%q default=%q, want %+v, nz, nz", m.Regions, m.Home, m.Default, want)
	}
}

// A node that goes quiet during the gets answers nothing after them either, so the read stops and still returns what it has.
func TestReadRegions_StopsAfterTwoMissedReplies(t *testing.T) {
	names := []string{"nz"}
	parents := []string{"*"}
	for i := range 14 {
		names = append(names, fmt.Sprintf("auckland-%02d", i))
		parents = append(parents, "nz")
	}
	cli, asked := fakeRegionCLI(t, names, parents, nil, "", "")
	quiet := false
	m, err := readRegions(context.Background(), func(cmd string) (string, error) {
		if strings.HasPrefix(cmd, "region get ") {
			quiet = true
		}
		if quiet {
			*asked = append(*asked, cmd)
			return "", fmt.Errorf("%w: quiet", ErrNoReply)
		}
		return cli(cmd)
	})
	if err != nil {
		t.Fatal(err)
	}
	gets := 0
	for _, c := range *asked {
		if strings.HasPrefix(c, "region get ") {
			gets++
		}
	}
	if gets != 2 || !m.Partial || len(m.Regions) < 2 {
		t.Errorf("%d gets, partial=%v, %d regions; want 2, partial and the regions read", gets, m.Partial, len(m.Regions))
	}
}

// The firmware's get falls back to a prefix match, so asking for a removed region can answer with another one.
func TestParseRegionGet_PrefixMatchIsNotTheRegion(t *testing.T) {
	for _, tc := range []struct {
		reply, name, parent string
		ok                  bool
	}{
		{" a (x) F", "a", "x", true},
		{" a F", "a", "*", true},
		{" a ", "a", "*", true},
		{" #a (#x) F", "a", "x", true},
		{" abc (x) F", "a", "", false},
		{" abc F", "a", "", false},
	} {
		got, err := parseRegionGet(tc.reply, tc.name)
		if (err == nil) != tc.ok || got != tc.parent {
			t.Errorf("parseRegionGet(%q, %q) = %q, %v; want %q, ok=%v", tc.reply, tc.name, got, err, tc.parent, tc.ok)
		}
	}
}

func TestReadRegions_CaretAmbiguousPastTheCut(t *testing.T) {
	names := []string{"a^"}
	parents := []string{"*"}
	for i := range 14 {
		names = append(names, fmt.Sprintf("akl-%02d", i))
		parents = append(parents, "a^")
	}
	names = append(names, "a")
	parents = append(parents, "*")
	cli, _ := fakeRegionCLI(t, names, parents, map[string]bool{"a": true}, "a", "")
	m, err := readRegions(context.Background(), cli)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]RegionEntry{}
	for _, r := range m.Regions {
		got[r.Name] = r
	}
	if got["a"] != (RegionEntry{"a", "*", true}) || got["a^"] != (RegionEntry{"a^", "*", false}) || got["akl-00"].Parent != "a^" {
		t.Errorf("a=%+v a^=%+v akl-00=%+v", got["a"], got["a^"], got["akl-00"])
	}
}
