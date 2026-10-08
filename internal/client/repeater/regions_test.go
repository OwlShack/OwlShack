package repeater

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"testing"
	"time"

	meshcore "github.com/OwlShack/meshcore-go"
	"github.com/OwlShack/meshcore-go/node"

	"github.com/OwlShack/OwlShack/internal/store"
)

// The firmware answers a REGIONS request only when it arrives direct (simple_repeater MyMesh.cpp onAnonDataRecv), and the reply comes back as a RESPONSE tagged with our timestamp.
func TestRequestRegions_DirectAndParsed(t *testing.T) {
	t.Parallel()
	self := meshcore.NewLocalIdentityFromSeed([32]byte{0x11})
	rpt := meshcore.NewLocalIdentityFromSeed([32]byte{0x22})
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	radio := captureRadio{sent: make(chan []byte, 4)}
	n := node.New(self, radio)
	t.Cleanup(n.Stop)
	rm := NewClient(n, st, 0, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, func() uint8 { return 2 }, func([]byte) *meshcore.Region { return nil })
	secret, err := rpt.SharedSecret(self.Identity)
	if err != nil {
		t.Fatal(err)
	}

	type result struct {
		reply meshcore.AnonRegionsReply
		err   error
	}
	done := make(chan result, 1)
	go func() {
		r, err := rm.RequestRegions(hex.EncodeToString(rpt.PublicKeyBytes()), 3*time.Second)
		done <- result{r, err}
	}()

	pkt, err := meshcore.PacketFromBytes(<-radio.sent)
	if err != nil {
		t.Fatal(err)
	}
	if pkt.PayloadType() != meshcore.PayloadTypeAnonReq || !pkt.IsRouteDirect() || pkt.PathHashCount() != 0 {
		t.Fatalf("sent type %d direct=%v hops=%d, want a zero-hop direct ANON_REQ", pkt.PayloadType(), pkt.IsRouteDirect(), pkt.PathHashCount())
	}
	req, err := meshcore.AnonReqFromBytes(pkt.Payload)
	if err != nil {
		t.Fatal(err)
	}
	plain := req.Decrypt(secret)
	if len(plain) < 6 || plain[4] != meshcore.AnonReqTypeRegions || plain[5] != meshcore.MakePathLen(2, 0) {
		t.Fatalf("plaintext %x: want [tag][0x01][empty reply path at 2 bytes a hop]", plain)
	}

	body := binary.LittleEndian.AppendUint32(nil, binary.LittleEndian.Uint32(plain[:4]))
	body = binary.LittleEndian.AppendUint32(body, 1791300000)
	body = append(body, "*,nz,akl"...)
	enc, err := meshcore.EncryptThenMAC(secret, body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&meshcore.Response{
		Destination: self.PublicKey()[0], Source: rpt.PublicKey()[0],
		MAC: [2]byte{enc[0], enc[1]}, EncryptedPayload: enc[2:],
	}).ToBytes()
	if err != nil {
		t.Fatal(err)
	}
	rm.HandleResponsePacket(&meshcore.Packet{Header: meshcore.MakeHeader(meshcore.RouteTypeDirect, meshcore.PayloadTypeResponse, 0), Payload: resp})

	got := <-done
	if got.err != nil {
		t.Fatal(got.err)
	}
	if !slices.Equal(got.reply.Regions, []string{"*", "nz", "akl"}) || got.reply.Clock != 1791300000 {
		t.Errorf("reply = %+v", got.reply)
	}
}
