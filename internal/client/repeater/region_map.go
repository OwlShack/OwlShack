package repeater

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// RegionMap is a remote node's regions as its CLI reports them, names without their "#" as the firmware's tree prints them.
type RegionMap struct {
	Regions          []RegionEntry `json:"regions"`          // "*" first
	Home             string        `json:"home"`             // "*" when none is set, as `region home` says
	Default          string        `json:"default"`          // "" for none (`<null>`)
	DefaultSupported bool          `json:"defaultSupported"` // false on firmware without `region default`
	Partial          bool          `json:"partial"`          // a region may be missing: a name list was cut or a reply lost
}

// RegionEntry is one region; Parent is "" for "*" and "*" at the top.
type RegionEntry struct {
	Name      string `json:"name"`
	Parent    string `json:"parent"`
	DenyFlood bool   `json:"denyFlood"`
}

// ErrNoRegions is the reply of a firmware without a region map.
var ErrNoRegions = errors.New("this firmware has no regions")

// cliReplyMax is what fits RegionMap::exportTo(reply, 160) and exportNamesTo(reply, 160): one byte goes to the NUL.
const cliReplyMax = 159

// listMaySkip is a name list long enough that exportNamesTo may have skipped a 30-byte name: it stops once written+30+2 reaches 160, and drops the last comma.
const listMaySkip = cliReplyMax - 32

// ReadRegions reads the tree with `region`, then fills in what a cut reply left out with the name lists and `region get`.
func (rm *Client) ReadRegions(ctx context.Context, pubkeyHex string, timeout time.Duration) (*RegionMap, error) {
	return readRegions(ctx, func(cmd string) (string, error) { return rm.SendCLI(pubkeyHex, cmd, timeout) })
}

func readRegions(ctx context.Context, send func(string) (string, error)) (*RegionMap, error) {
	// Each command is a mesh round trip, so a request the caller left stops between them.
	cli := func(cmd string) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		reply, err := send(cmd)
		if err != nil {
			return "", fmt.Errorf("%s: %w", cmd, err)
		}
		return reply, nil
	}
	tree, err := cli("region")
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(tree, "*") { // the tree always starts at the wildcard
		return nil, ErrNoRegions
	}
	// A name may end in "^" as the home marker does, so the tree is read knowing the home.
	reply, err := cli("region home")
	if err != nil {
		return nil, err
	}
	home, ok := strings.CutPrefix(reply, " home is ")
	if !ok {
		return nil, fmt.Errorf("%w: region home: %q", ErrBadReply, reply)
	}
	home = strings.TrimPrefix(home, "#")
	m, cut, homeLines := parseRegionTree(tree, home)
	m.Home = home
	// Asked before the lists, so a node that goes quiet during them still leaves a partial map.
	reply, err = cli("region default")
	if err != nil {
		return nil, err
	}
	if name, ok := strings.CutPrefix(reply, " default scope is "); ok {
		name = strings.TrimPrefix(name, "#")
		if name == "<null>" {
			name = ""
		}
		m.Default, m.DefaultSupported = name, true
	}
	if cut || homeLines > 1 {
		lists, err := readNameLists(cli, m)
		if err != nil {
			return nil, err
		}
		// Home a and a region a^ both print "a^": with two such lines, or one and the other past the cut, only `region get` says which is which.
		if homeLines > 1 || homeLines == 1 && slices.ContainsFunc(lists, func(n listedName) bool { return n.name == home+"^" }) {
			m.Regions = m.Regions[:1]
		}
		if err := fillRegions(ctx, cli, m, lists); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// parseRegionTree reads RegionMap::printChildRegions (indent per level, "^" home, " F" flood); homeLines counts lines that read as home, as home a and region a^ both print "a^".
func parseRegionTree(s, home string) (m *RegionMap, cut bool, homeLines int) {
	m = &RegionMap{}
	cut = len(s) >= cliReplyMax
	lines := strings.Split(s, "\n")
	lines = lines[:len(lines)-1] // after the last newline: empty, or a line the cut broke
	var stack []string
	for _, line := range lines {
		name := strings.TrimLeft(line, " ")
		indent := len(line) - len(name)
		if name == "" || indent > len(stack) {
			continue
		}
		deny := true
		if n, ok := strings.CutSuffix(name, " F"); ok {
			name, deny = n, false
		}
		name = strings.TrimPrefix(name, "#")
		if name == home+"^" {
			name = home
			homeLines++
		}
		parent := ""
		if indent > 0 {
			parent = stack[indent-1]
		}
		stack = append(stack[:indent], name)
		m.Regions = append(m.Regions, RegionEntry{Name: name, Parent: parent, DenyFlood: deny})
	}
	return m, cut, homeLines
}

// listedName is one name from `region list allowed|denied`.
type listedName struct {
	name   string
	denied bool
}

// readNameLists reads which regions exist and their flood; a list long enough to have skipped a name makes the map partial.
func readNameLists(cli func(string) (string, error), m *RegionMap) ([]listedName, error) {
	var names []listedName
	for _, which := range []string{"allowed", "denied"} {
		reply, err := cli("region list " + which)
		if err != nil {
			return nil, err
		}
		if len(reply) >= listMaySkip {
			m.Partial = true
		}
		if reply == "-none-" {
			continue
		}
		for name := range strings.SplitSeq(reply, ",") {
			if name = strings.TrimPrefix(name, "#"); name != "" {
				names = append(names, listedName{name, which == "denied"})
			}
		}
	}
	return names, nil
}

// fillRegions asks `region get` for the parent of each listed region the map lacks; two missed replies in a row end it, partial.
func fillRegions(ctx context.Context, cli func(string) (string, error), m *RegionMap, names []listedName) error {
	have := make(map[string]bool, len(m.Regions))
	for _, r := range m.Regions {
		have[r.Name] = true
	}
	missed := 0
	for _, n := range names {
		if have[n.name] {
			continue
		}
		got, err := cli("region get " + n.name)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil && !errors.Is(err, ErrNoReply) {
			return err // the login or the link is gone: the read can't go on
		}
		if err != nil { // lost: leave it out and say the map may be short
			m.Partial = true
			if missed++; missed == 2 {
				return nil
			}
			continue
		}
		missed = 0
		parent, perr := parseRegionGet(got, n.name)
		if perr != nil { // removed since the list was read
			m.Partial = true
			continue
		}
		have[n.name] = true
		m.Regions = append(m.Regions, RegionEntry{Name: n.name, Parent: parent, DenyFlood: n.denied})
	}
	return nil
}

// parseRegionGet reads " name (parent) F" or " name F" (flag empty when denied) and returns the parent, "*" at the top.
func parseRegionGet(reply, name string) (string, error) {
	rest, ok := strings.CutPrefix(reply, " ")
	if !ok {
		return "", fmt.Errorf("region get %s: %q", name, reply)
	}
	rest = strings.TrimPrefix(rest, "#")
	rest, ok = strings.CutPrefix(rest, name)
	if !ok || rest != "" && rest[0] != ' ' { // the firmware falls back to a prefix match, which names another region
		return "", fmt.Errorf("region get %s: %q", name, reply)
	}
	if p, ok := strings.CutPrefix(rest, " ("); ok {
		if end := strings.Index(p, ")"); end > 0 {
			return strings.TrimPrefix(p[:end], "#"), nil
		}
	}
	return "*", nil
}
