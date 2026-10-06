package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OwlShack/OwlShack/internal/store"
)

// refServer wires a real store to a mux with one echo route, so what a handler sees after the
// rewrite is what a /api/companions/{name} handler would see.
func refServer(t *testing.T, names ...string) (*Server, func(path string) (int, string)) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "ref.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	for _, n := range names {
		c := store.Companion{Name: n}
		st.WriteSync(func() {
			if err := st.Companions.Create(context.Background(), &c); err != nil {
				t.Fatalf("creating companion %q: %v", n, err)
			}
		})
	}

	s := &Server{store: st, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /api/companions/{name}/messages", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(r.PathValue("name")))
	})
	s.mux.HandleFunc("GET /api/peers/{name}", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(r.PathValue("name")))
	})
	// A deeper route, to show whether a rewrite can move a request onto one it did not address.
	s.mux.HandleFunc("GET /api/companions/{name}/repeaters/{pubkey}/cli", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("CLI:" + r.PathValue("name") + ":" + r.PathValue("pubkey")))
	})

	return s, func(path string) (int, string) {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code, rec.Body.String()
	}
}

func TestCompanionRef_ResolvesToTheCurrentName(t *testing.T) {
	t.Parallel()
	_, get := refServer(t, "🐶Akl")

	for _, tc := range []struct{ name, path, want string }{
		{"id and slug", "/api/companions/1-akl/messages", "🐶Akl"},
		{"bare id", "/api/companions/1/messages", "🐶Akl"},
		{"stale slug still resolves", "/api/companions/1-some-old-name/messages", "🐶Akl"},
		{"escaped name still works", "/api/companions/" + url.PathEscape("🐶Akl") + "/messages", "🐶Akl"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, body := get(tc.path)
			if code != http.StatusOK || body != tc.want {
				t.Errorf("%s = %d %q, want 200 %q", tc.path, code, body, tc.want)
			}
		})
	}
}

// An unknown id must reach the handler unchanged so it answers "companion not found" rather than
// silently addressing whichever companion happens to be first.
func TestCompanionRef_UnknownIDIsLeftAlone(t *testing.T) {
	t.Parallel()
	_, get := refServer(t, "🐶Akl")
	if _, body := get("/api/companions/99-akl/messages"); body != "99-akl" {
		t.Errorf("name = %q, want the segment passed through unchanged", body)
	}
}

// A numeric name is a real possibility on a mesh, and its own URL must win over an id that collides.
func TestCompanionRef_NamePreferredOverCollidingID(t *testing.T) {
	t.Parallel()
	_, get := refServer(t, "Akl", "1")

	if _, body := get("/api/companions/1/messages"); body != "1" {
		t.Errorf("name = %q, want the companion actually named \"1\"", body)
	}
	// The companion named "1" is id 2, so its own ref still reaches it.
	if _, body := get("/api/companions/2-1/messages"); body != "1" {
		t.Errorf("ref 2-1 = %q, want \"1\"", body)
	}
}

// The rewrite is scoped to companion paths; a numeric segment anywhere else must not be touched.
func TestCompanionRef_OnlyRewritesCompanionPaths(t *testing.T) {
	t.Parallel()
	_, get := refServer(t, "🐶Akl")
	if _, body := get("/api/peers/1-akl"); body != "1-akl" {
		t.Errorf("peers segment = %q, want it untouched", body)
	}
}

func TestParseCompanionRef(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		seg    string
		wantID int64
		wantOK bool
	}{
		{"1", 1, true},
		{"12-akl", 12, true},
		{"1-", 1, true},
		{"🐶Akl", 0, false},
		{"Akl-1", 0, false},
		{"", 0, false},
		{"-1", 0, false},
		{"0", 0, false},
		{"0-x", 0, false},
		{"1.5", 0, false},
		{"01", 1, true},
	} {
		id, ok := parseCompanionRef(tc.seg)
		if id != tc.wantID || ok != tc.wantOK {
			t.Errorf("parseCompanionRef(%q) = %d,%v; want %d,%v", tc.seg, id, ok, tc.wantID, tc.wantOK)
		}
	}
}

// A path segment is one segment however it is spelled: an encoded slash inside the ref must not
// let the request cross into a route its escaped path never addressed.
func TestCompanionRef_EncodedSlashCannotCrossRoutes(t *testing.T) {
	t.Parallel()
	_, get := refServer(t, "🐶Akl")

	code, body := get("/api/companions/1-a%2Frepeaters%2FDEAD/cli")
	if strings.HasPrefix(body, "CLI:") {
		t.Errorf("reached the repeater CLI route (%d %q); the request addressed {name}/cli", code, body)
	}
}

// Same rule on the way out: a companion whose name contains a slash must not spread across
// segments when it is substituted back into the path.
func TestCompanionRef_NameWithSlashStaysOneSegment(t *testing.T) {
	t.Parallel()
	_, get := refServer(t, "evil/repeaters/DEAD")

	if code, body := get("/api/companions/1/cli"); strings.HasPrefix(body, "CLI:") {
		t.Errorf("a companion name steered the request onto the CLI route (%d %q)", code, body)
	}
	// And it must still be reachable by its own ref.
	if code, body := get("/api/companions/1-evil-repeaters-dead/messages"); body != "evil/repeaters/DEAD" {
		t.Errorf("name = %d %q, want %q", code, body, "evil/repeaters/DEAD")
	}
}
