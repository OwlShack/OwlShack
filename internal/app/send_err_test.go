package app

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/OwlShack/meshcore-go/node"

	"github.com/OwlShack/OwlShack/internal/api"
	"github.com/OwlShack/OwlShack/internal/node/companion"
)

// A refused send is busy or the request's fault, never a 500 that reads as a crash.
func TestSendErr_Statuses(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{
		{fmt.Errorf("x: %w", node.ErrTxQueueFull), http.StatusServiceUnavailable},
		{fmt.Errorf("x: %w", node.ErrTextTooLong), http.StatusUnprocessableEntity},
		{fmt.Errorf("x: %w", companion.ErrUnknownChannel), http.StatusNotFound},
	} {
		var serr *api.StatusError
		if !errors.As(sendErr(tc.err), &serr) || serr.Status != tc.want {
			t.Errorf("sendErr(%v) status = %+v, want %d", tc.err, serr, tc.want)
		}
	}
	// A packet that can't be built usually means a bad stored route, a fault here rather than in the request.
	var serr *api.StatusError
	if errors.As(sendErr(fmt.Errorf("x: %w", node.ErrInvalidPacket)), &serr) {
		t.Errorf("invalid packet answered %d, want a plain 500", serr.Status)
	}
	if err := sendErr(nil); err != nil {
		t.Errorf("sendErr(nil) = %v", err)
	}
}
