package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type editBackend struct {
	Backend
	editErr, persistErr error
	moved, flood        int
}

func (b *editBackend) ChannelMutator(string) (ChannelAdder, ChannelRemover, bool) {
	return func(string, string) error { return b.editErr }, func(string) error { return b.editErr }, true
}
func (b *editBackend) RenameChannel(string, string, string) error { return b.editErr }
func (b *editBackend) PersistChannels(context.Context) error      { return b.persistErr }
func (b *editBackend) MoveRepeaterRegion(context.Context, string, string) error {
	b.moved++
	return b.editErr
}
func (b *editBackend) SetRepeaterRegionFlood(context.Context, string, bool) error {
	b.flood++
	return b.editErr
}
func (b *editBackend) DeleteRepeater(context.Context) error { return b.editErr }

func serve(b Backend, method, url, body string) int {
	s := &Server{mux: http.NewServeMux(), log: slog.New(slog.DiscardHandler)}
	s.routes()
	s.SetBackend(b)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(method, url, strings.NewReader(body)))
	return rec.Code
}

// A channel edit says whose fault a refusal is, and a change that isn't saved is a 500, as it reverts at restart.
func TestChannelEdit_Statuses(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name               string
		editErr, persisted error
		want               int
	}{
		{"done", nil, nil, http.StatusNoContent},
		{"request at fault", Invalid(errors.New("bad name")), nil, http.StatusUnprocessableEntity},
		{"not found", Failed(http.StatusNotFound, errors.New("no such channel")), nil, http.StatusNotFound},
		{"refused", errors.New("in use by a bot"), nil, http.StatusConflict},
		{"not saved", nil, errors.New("disk full"), http.StatusInternalServerError},
	} {
		b := &editBackend{editErr: tc.editErr, persistErr: tc.persisted}
		for _, req := range []struct{ method, url, body string }{
			{http.MethodPost, "/api/companions/c/channels", `{"name":"#x"}`},
			{http.MethodPatch, "/api/companions/c/channels/Public", `{"name":"General"}`},
			{http.MethodDelete, "/api/companions/c/channels/x", ``},
		} {
			if got := serve(b, req.method, req.url, req.body); got != tc.want {
				t.Errorf("%s: %s %s = %d, want %d", tc.name, req.method, req.url, got, tc.want)
			}
		}
	}
}

// A region PATCH changes one thing, named explicitly; an empty body used to mean "allow flood".
func TestRepeaterRegionPatch_OneChangeAtATime(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		body         string
		want         int
		moved, flood int
	}{
		{`{}`, http.StatusBadRequest, 0, 0},
		{`{"parent":"*","denyFlood":true}`, http.StatusBadRequest, 0, 0},
		{`{"parent":"*"}`, http.StatusNoContent, 1, 0},
		{`{"denyFlood":true}`, http.StatusNoContent, 0, 1},
	} {
		b := &editBackend{}
		if got := serve(b, http.MethodPatch, "/api/config/repeater/regions/nz", tc.body); got != tc.want || b.moved != tc.moved || b.flood != tc.flood {
			t.Errorf("%s: %d (moved %d, flood %d), want %d (%d, %d)", tc.body, got, b.moved, b.flood, tc.want, tc.moved, tc.flood)
		}
	}
	b := &editBackend{editErr: Failed(http.StatusNotFound, errors.New("unknown region"))}
	if got := serve(b, http.MethodPatch, "/api/config/repeater/regions/nope", `{"parent":"*"}`); got != http.StatusNotFound {
		t.Errorf("moving an unknown region: %d, want 404", got)
	}
}

// A refused repeater delete keeps its status, so "it feeds MQTT" reads as a conflict, not bad input.
func TestDeleteRepeater_KeepsTheRefusalStatus(t *testing.T) {
	t.Parallel()
	b := &editBackend{editErr: Failed(http.StatusConflict, errors.New("the repeater feeds MQTT"))}
	if got := serve(b, http.MethodDelete, "/api/config/repeater", ""); got != http.StatusConflict {
		t.Errorf("DELETE /api/config/repeater = %d, want 409", got)
	}
}
