package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/OwlShack/OwlShack/internal/app"
	"github.com/OwlShack/OwlShack/internal/buildinfo"
	"github.com/OwlShack/OwlShack/internal/logging"
	flag "github.com/spf13/pflag"
)

func main() {
	configPath := flag.StringP("config", "c", "", "import a config file (toml, yaml, or json) into the database and run with it")
	showVersion := flag.BoolP("version", "V", false, "print version and exit")
	verbosity := flag.CountP("verbose", "v", "increase log verbosity (-v=debug, -vv=trace, -vvv=trace+)")
	flag.Parse()

	if *showVersion {
		// Same shape as the repeater's ver reply, so a stamp can be checked without a radio.
		fmt.Printf("OwlShack %s (Build: %s)\n", buildinfo.Version, buildinfo.Date)
		return
	}

	// Configured again inside app.Run once the stored log level is known.
	logging.Configure(*verbosity, "")

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := app.Run(ctx, *configPath, *verbosity); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}
