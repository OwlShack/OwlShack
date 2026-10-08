package app

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/OwlShack/OwlShack/internal/api"
	repeaterclient "github.com/OwlShack/OwlShack/internal/client/repeater"
)

// Each way a remote request fails gets the status that says what to do about it; only the unknown is the server's.
func TestRemoteErr(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		err  error
		want int
	}{
		{fmt.Errorf("%w: hex: odd length", repeaterclient.ErrBadPubkey), http.StatusBadRequest},
		{repeaterclient.ErrUnknownPeer, http.StatusNotFound},
		{fmt.Errorf("%w to this repeater", repeaterclient.ErrNotLoggedIn), http.StatusUnauthorized},
		{repeaterclient.ErrNotAdmin, http.StatusForbidden},
		{fmt.Errorf("CLI command timed out after 10s: %w", repeaterclient.ErrNoReply), http.StatusGatewayTimeout},
		{fmt.Errorf("%w: status data too short", repeaterclient.ErrBadReply), http.StatusBadGateway},
		{repeaterclient.ErrBusy, http.StatusServiceUnavailable},
		{repeaterclient.ErrNoDirectRoute, http.StatusConflict},
		{fmt.Errorf("setperm %w: nope", repeaterclient.ErrRejected), http.StatusUnprocessableEntity},
		{repeaterclient.ErrRegionLoad, http.StatusUnprocessableEntity},
		{repeaterclient.ErrNoRegions, http.StatusUnprocessableEntity},
		{errors.New("sending CLI: radio gone"), http.StatusInternalServerError},
	} {
		got := http.StatusInternalServerError
		var serr *api.StatusError
		var verr *api.ValidationError
		switch err := remoteErr(tc.err); {
		case errors.As(err, &serr):
			got = serr.Status
		case errors.As(err, &verr):
			got = http.StatusUnprocessableEntity
		}
		if got != tc.want {
			t.Errorf("%v -> %d, want %d", tc.err, got, tc.want)
		}
	}
	if remoteErr(nil) != nil {
		t.Error("nil became an error")
	}
}
