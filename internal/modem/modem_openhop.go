package modem

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/OwlShack/OwlShack/internal/config"
	"github.com/OwlShack/meshcore-go/hardware"
	"github.com/OwlShack/meshcore-go/hardware/openhop"
)

// setupOpenhop drives openHop Modem firmware, which speaks its own protocol rather than KISS: it
// owns the radio and does CAD itself, so this process only frames packets.
func setupOpenhop(ctx context.Context, ms *State, cfg *config.Config, connAddr string, radioConfig *hardware.RadioConfig) error {
	var token string
	var dial openhop.Dialer
	switch {
	case strings.HasPrefix(connAddr, "/"):
		// Auth is a TCP-client concept; the firmware never asks a serial client for a token.
		dial = openhop.SerialDialer(connAddr, *cfg.BaudRate)
	default:
		if cfg.ModemToken != nil {
			token = *cfg.ModemToken
		}
		dial = openhop.TCPDialer(connAddr, 0)
	}

	// PreambleLen is derived, not taken from the modem: openHop's own default is tuned for its
	// firmware, and a preamble MeshCore does not use puts us off the air with every node.
	radio := openhop.RadioConfig{
		FreqHz:      radioConfig.FreqHz,
		BandwidthHz: radioConfig.BwHz,
		SF:          radioConfig.SF,
		CR:          radioConfig.CR,
		TxPower:     int8(*cfg.TX),
		SyncWord:    openhop.MeshCoreSyncWord,
		PreambleLen: uint8(hardware.PreambleForSF(radioConfig.SF)),
	}

	m := openhop.New(dial,
		openhop.Config{Token: token, Radio: radio},
		openhop.WithLogger(slog.Default()),
		openhop.WithErrorHandler(func(err error) {
			slog.Warn("modem error", "component", "modem", "error", err)
		}),
	)
	m.SetLogHandler(func(l openhop.LogLevel, text string) {
		slog.Debug("modem log", "component", "modem", "level", l.String(), "text", text)
	})

	connectCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := m.Connect(connectCtx); err != nil {
		m.Close()
		return fmt.Errorf("openhop connect: %w", err)
	}
	ms.closers = append(ms.closers, m)

	slog.Info("radio up", "component", "modem", "transport", "openhop", "target", connAddr,
		"freq", *cfg.Freq, "bw", *cfg.Bw, "sf", *cfg.SF, "cr", *cfg.CR, "tx", *cfg.TX,
		"preamble_symbols", radio.PreambleLen)

	ms.Stats = NewOpenhopStatsProvider(m, RadioInfo{
		FreqHz:  radioConfig.FreqHz,
		BwHz:    radioConfig.BwHz,
		SF:      radioConfig.SF,
		CR:      radioConfig.CR,
		TxPower: *cfg.TX,
	})
	ms.Modem = m
	return nil
}
