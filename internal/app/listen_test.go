package app

import (
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OwlShack/OwlShack/internal/config"
	"github.com/OwlShack/OwlShack/internal/store"
)

// A taken web port must stop Run, not leave a node running with no UI.
func TestRun_BindFailureIsFatal(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	_, port, err := net.SplitHostPort(busy.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	t.Chdir(t.TempDir())
	t.Setenv("HOST", "127.0.0.1")
	t.Setenv("PORT", port)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err = Run(ctx, "", 0)
	if err == nil {
		t.Fatal("Run returned nil with the web port already taken")
	}
	if !strings.Contains(err.Error(), "web listen on 127.0.0.1:"+port) {
		t.Fatalf("error is not the bind failure: %v", err)
	}
}

func seedStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "seed.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := initConfigTables(context.Background(), db); err != nil {
		t.Fatalf("initConfigTables: %v", err)
	}
	return db
}

// LISTEN_DEFAULT seeds an unset address, so the Settings page can still move it afterwards.
func TestSeedListenAddr_SeedsOnlyWhenUnset(t *testing.T) {
	db := seedStore(t)
	t.Setenv("LISTEN_DEFAULT", ":8860")

	cfg := &config.Config{}
	if err := seedListenAddr(context.Background(), db, cfg); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if cfg.ListenAddr == nil || *cfg.ListenAddr != ":8860" {
		t.Fatalf("config not seeded: %v", cfg.ListenAddr)
	}
	s, err := db.Settings.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.ListenAddr == nil || *s.ListenAddr != ":8860" {
		t.Fatalf("not persisted, so Settings would still show nothing: %v", s.ListenAddr)
	}

	// What Settings does next: store another address, and the seed must not take it back.
	chosen := ":9999"
	cfg.ListenAddr = &chosen
	s.ListenAddr = &chosen
	db.WriteSync(func() { err = db.Settings.Set(context.Background(), s) })
	if err != nil {
		t.Fatal(err)
	}
	if err := seedListenAddr(context.Background(), db, cfg); err != nil {
		t.Fatalf("seed: %v", err)
	}
	s, err = db.Settings.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.ListenAddr == nil || *s.ListenAddr != ":9999" {
		t.Fatalf("seed overwrote the configured address: %v", s.ListenAddr)
	}
}

func TestSeedListenAddr_RejectsGarbage(t *testing.T) {
	db := seedStore(t)
	t.Setenv("LISTEN_DEFAULT", "8860")
	if err := seedListenAddr(context.Background(), db, &config.Config{}); err == nil {
		t.Fatal("a bare port was accepted; it would fail later at bind time instead")
	}
}
