package app

import (
	"context"
	"fmt"

	"github.com/OwlShack/OwlShack/internal/signaltest"
)

// newSignalTestTracer adapts RunTrace to signaltest.Tracer so signaltest never imports internal/node/companion.
func newSignalTestTracer(reg *companionRegistry) signaltest.Tracer {
	return func(ctx context.Context, name string, path []byte, hashSize uint8) (*signaltest.TraceResult, error) {
		c, ok := reg.find(name)
		if !ok {
			return nil, fmt.Errorf("companion %q is not running", name)
		}
		out, err := c.RunTrace(ctx, path, hashSize)
		if err != nil {
			return nil, err
		}
		return &signaltest.TraceResult{
			HopSNRs:   out.HopSNRs,
			SNR:       out.SNR,
			ElapsedMs: out.Elapsed.Milliseconds(),
			Complete:  out.Complete,
		}, nil
	}
}
