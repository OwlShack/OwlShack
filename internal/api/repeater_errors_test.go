package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type cliBackend struct {
	Backend
	err error
}

func (b cliBackend) Repeater(string) (*RepeaterOps, bool) {
	return &RepeaterOps{
		CLI:     func(string, string) (string, error) { return "", b.err },
		PathGet: func(string) (any, error) { return nil, b.err },
	}, true
}

// A remote request fails for a reason the operator can act on, and the status says which; only the unexpected is a 500.
func TestRemoteErrorStatus(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"no reply", Failed(http.StatusGatewayTimeout, errors.New("CLI command timed out after 10s: the node did not answer")), http.StatusGatewayTimeout},
		{"not logged in", Failed(http.StatusUnauthorized, errors.New("not logged in to this repeater")), http.StatusUnauthorized},
		{"refused", Invalid(errors.New("region load can't run over the mesh")), http.StatusUnprocessableEntity},
		{"unexpected", errors.New("disk on fire"), http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &Server{mux: http.NewServeMux(), log: slog.New(slog.NewTextHandler(io.Discard, nil))}
			s.routes()
			s.SetBackend(cliBackend{err: tc.err})
			for _, req := range []*http.Request{
				httptest.NewRequest(http.MethodPost, "/api/companions/c/repeaters/aa/cli", strings.NewReader(`{"command":"ver"}`)),
				httptest.NewRequest(http.MethodGet, "/api/companions/c/contacts/aa/path", nil),
			} {
				rec := httptest.NewRecorder()
				s.mux.ServeHTTP(rec, req)
				if rec.Code != tc.want || !strings.Contains(rec.Body.String(), tc.err.Error()) {
					t.Errorf("%s %s = %d %s, want %d with the reason", req.Method, req.URL.Path, rec.Code, rec.Body.String(), tc.want)
				}
			}
		})
	}
}

// A browser that left mid-request isn't a server failure.
func TestRemoteErrorStatus_CancelledIsNotA500(t *testing.T) {
	t.Parallel()
	s := &Server{mux: http.NewServeMux(), log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.routes()
	s.SetBackend(cliBackend{err: fmt.Errorf("region: %w", context.Canceled)})
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/companions/c/repeaters/aa/cli", strings.NewReader(`{"command":"ver"}`)))
	if rec.Code == http.StatusInternalServerError {
		t.Errorf("a cancelled request answered %d", rec.Code)
	}
}
