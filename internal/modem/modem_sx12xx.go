package modem

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/OwlShack/OwlShack/internal/config"
	"github.com/OwlShack/meshcore-go/hardware"
	"github.com/OwlShack/meshcore-go/hardware/sx12xx"
	"periph.io/x/conn/v3/gpio/gpioreg"
	"periph.io/x/conn/v3/spi/spireg"
	"periph.io/x/host/v3"
)

// periphOnce guards host.Init; a reconnect re-opens the SPI port, not the host.
var periphOnce struct {
	sync.Once
	err error
}

func initPeriph() error {
	periphOnce.Do(func() {
		if _, err := host.Init(); err != nil {
			periphOnce.err = fmt.Errorf("periph host init: %w", err)
		}
	})
	return periphOnce.err
}

// spiPortKey reduces "SPI0.1" and "/dev/spidev0.1" to "0.1", so the same device spelled two ways
// does not read as a mismatch.
func spiPortKey(s string) string {
	s = strings.TrimPrefix(s, "/dev/spidev")
	s = strings.TrimPrefix(s, "SPI")
	return s
}

// setupSPI drives an SX12xx wired straight to the host's SPI bus, with no MeshCore firmware in front of the chip, so this process is the radio stack.
func setupSPI(ms *State, cfg *config.Config, connAddr string, radioConfig *hardware.RadioConfig) error {
	if cfg.SPIBoard == nil || *cfg.SPIBoard == "" {
		return fmt.Errorf("spi connection needs spiBoard set (known: %v)", BoardNames())
	}
	board, err := LookupBoard(*cfg.SPIBoard)
	if err != nil {
		return err
	}
	if board.Verified != "hardware" {
		slog.Warn("board wiring has not been verified on hardware here",
			"component", "modem", "board", board.Name, "provenance", board.Verified,
			"notes", board.Notes)
	}

	// Past the module's rating the PA cooks, so this is a hard error rather than a clamp.
	txPower := *cfg.TX
	if txPower > board.MaxTxPower {
		return fmt.Errorf("tx power %d dBm exceeds %s maximum of %d dBm",
			txPower, board.Label, board.MaxTxPower)
	}

	if err := initPeriph(); err != nil {
		return err
	}

	portName := connAddr
	if portName == "" {
		portName = board.SPIPort
	}
	// Talking to the wrong chip-select reads back as a chip that never answers, which the driver
	// reports as bad wiring — so say plainly that the two disagree before that error appears.
	if connAddr != "" && board.SPIPort != "" && spiPortKey(connAddr) != spiPortKey(board.SPIPort) {
		slog.Warn("connection names a different SPI device than this board's chip-select",
			"component", "modem", "board", board.Name,
			"connection", connAddr, "boardExpects", board.SPIPort)
	}
	port, err := spireg.Open(portName)
	if err != nil {
		return fmt.Errorf("open spi %s: %w", portName, err)
	}
	ms.closers = append(ms.closers, port)

	opts := board.Opts()
	dropMissingLEDs(&opts, board.Name)
	radio, err := sx12xx.NewSX126x(port, &opts)
	if err != nil {
		ms.Close()
		return fmt.Errorf("sx126x on %s: %w", portName, err)
	}

	if de, err := radio.DeviceErrors(); err != nil {
		slog.Warn("radio device errors unreadable", "component", "modem", "error", err)
	} else if de != 0 {
		slog.Error("radio reports device errors", "component", "modem",
			"errors", fmt.Sprintf("%#04x", de), "hint", "check TCXO voltage and frequency band")
	}

	stats := NewSx12xxStatsProvider(RadioInfo{
		FreqHz:  radioConfig.FreqHz,
		BwHz:    radioConfig.BwHz,
		SF:      radioConfig.SF,
		CR:      radioConfig.CR,
		TxPower: txPower,
	})

	m, err := sx12xx.NewModem(radio, radioConfig,
		sx12xx.WithTxPower(int(txPower)),
		sx12xx.WithModemLogger(slog.Default()),
		// The KISS threshold, so handler_slow means the same on both paths.
		sx12xx.WithHandlerWatchdog(handlerWatchdog),
		// NoteError owns driver_errors, so a driver fault must not also land in ParseErrors.
		sx12xx.WithModemErrorHandler(func(err error) {
			stats.NoteError(err)
		}),
	)
	if err != nil {
		ms.Close()
		return fmt.Errorf("sx12xx modem: %w", err)
	}
	ms.closers = append(ms.closers, m)
	stats.Attach(m)

	pre, payload := sx12xx.PacketWindows(radioConfig)
	slog.Info("radio up", "component", "modem", "board", board.Name, "chip", board.Chip,
		"leds", board.HasLEDs(),
		"spi", portName, "freq", *cfg.Freq, "bw", *cfg.Bw, "sf", *cfg.SF, "cr", *cfg.CR,
		"tx", txPower, "preamble_symbols", hardware.PreambleForSF(radioConfig.SF),
		"activity_window", pre+payload)

	ms.Stats = stats
	ms.Modem = m
	return nil
}

// dropMissingLEDs clears LED pins this host does not have: the driver rejects an unknown pin name, and a cosmetic pin from an unverified board entry must not stop the radio.
func dropMissingLEDs(o *sx12xx.Opts, board string) {
	for _, led := range []struct {
		role string
		pin  *string
	}{{"tx", &o.TxLedPin}, {"rx", &o.RxLedPin}} {
		if *led.pin == "" || gpioreg.ByName(*led.pin) != nil {
			continue
		}
		slog.Warn("activity LED pin not present on this host, LED disabled",
			"component", "modem", "board", board, "role", led.role, "pin", *led.pin)
		*led.pin = ""
	}
}
