package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/OwlShack/OwlShack/internal/api"
	"github.com/OwlShack/OwlShack/internal/store"
)

// Restore is a whole-file swap, so it is reachable only from the setup wizard: merging into a live node collides.

func pruneFrom(in api.BackupOptions) store.PruneOptions {
	ids := []int64{}
	if in.CompanionIDs != nil {
		ids = *in.CompanionIDs
	}
	return store.PruneOptions{
		CompanionIDs: ids,
		Contacts:     in.Contacts,
		Triggers:     in.Triggers,
		Mqtt:         in.Mqtt,
		Repeater:     in.Repeater,
		Peers:        in.Peers,
		MessageDays:  in.MessageDays,
		PacketDays:   in.PacketDays,
		MetricDays:   in.MetricDays,
		IdentityKeys: in.IdentityKeys,
	}
}

func (b *backend) ExportBackup(ctx context.Context, opts api.BackupOptions) (*api.BackupFile, error) {
	if b.db == nil {
		return nil, fmt.Errorf("database unavailable")
	}
	if err := validateOptions(opts); err != nil {
		return nil, err
	}

	tmp, err := reserveTempName()
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp)

	if err := b.db.BackupTo(ctx, tmp); err != nil {
		return nil, err
	}
	if err := store.PruneBackup(ctx, tmp, pruneFrom(opts)); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(tmp)
	if err != nil {
		return nil, fmt.Errorf("reading backup: %w", err)
	}
	return &api.BackupFile{
		Name:        fmt.Sprintf("owlshack-backup-%s.db", time.Now().Format("2006-01-02-1504")),
		ContentType: "application/vnd.sqlite3",
		Data:        data,
	}, nil
}

func validateOptions(opts api.BackupOptions) error {
	if opts.CompanionIDs == nil {
		return fmt.Errorf("companionIds is required: send every companion id to " +
			"include them all, or [] for a settings-only backup")
	}
	for _, d := range []struct {
		name string
		days int
	}{
		{"messageDays", opts.MessageDays},
		{"packetDays", opts.PacketDays},
		{"metricDays", opts.MetricDays},
	} {
		if d.days < store.DaysAll {
			return fmt.Errorf("%s must be -1 (all), 0 (none) or a number of days", d.name)
		}
	}
	return nil
}

// reserveTempName returns a free path because VACUUM INTO refuses to write to an existing file.
func reserveTempName() (string, error) {
	f, err := os.CreateTemp("", "owlshack-backup-*.db")
	if err != nil {
		return "", fmt.Errorf("preparing backup: %w", err)
	}
	name := f.Name()
	f.Close()
	if err := os.Remove(name); err != nil {
		return "", fmt.Errorf("preparing backup: %w", err)
	}
	return name, nil
}

// EstimateBackup reports what the current options would capture, before downloading.
func (b *backend) EstimateBackup(ctx context.Context, opts api.BackupOptions) (*api.BackupEstimate, error) {
	if b.db == nil {
		return nil, fmt.Errorf("database unavailable")
	}
	if err := validateOptions(opts); err != nil {
		return nil, err
	}
	c, err := b.db.CountForBackup(ctx, pruneFrom(opts))
	if err != nil {
		return nil, err
	}
	return &api.BackupEstimate{
		Companions: c.Companions,
		Contacts:   c.Contacts,
		Messages:   c.Messages,
		Packets:    c.Packets,
		Peers:      c.Peers,
		Metrics:    c.Metrics,
		Bytes:      c.Bytes,
	}, nil
}

// ImportBackup stages a database for the next startup (this process holds the live one open); anything else is treated as a config file.
func (b *backend) ImportBackup(ctx context.Context, data []byte, filename string) (*api.ImportResult, error) {
	if b.db == nil {
		return nil, fmt.Errorf("database unavailable")
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("no file was uploaded")
	}

	if strings.HasPrefix(string(data), "SQLite format 3\x00") {
		schema, err := store.StageRestore(ctx, dbPath, data)
		if err != nil {
			return nil, err
		}
		return &api.ImportResult{
			Kind:            "database",
			RestartRequired: true,
			SchemaVersion:   schema,
			Detail: "Backup accepted. Restart OwlShack to finish restoring; " +
				"the current database is kept as meshcore.db.replaced.",
		}, nil
	}
	return b.importConfigUpload(ctx, data, filename)
}

// importConfigUpload writes the upload to a temp file to reuse the -config loader.
func (b *backend) importConfigUpload(ctx context.Context, data []byte, filename string) (*api.ImportResult, error) {
	// LoadFromPath picks its parser from the extension; default to JSON so an unknown name gives a parse error.
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".json", ".toml", ".yaml", ".yml":
	default:
		ext = ".json"
	}
	dir, err := os.MkdirTemp("", "owlshack-import-*")
	if err != nil {
		return nil, fmt.Errorf("reading upload: %w", err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "config"+ext)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return nil, fmt.Errorf("reading upload: %w", err)
	}

	cfg, err := importConfigFile(ctx, b.db, path)
	if err != nil {
		return nil, fmt.Errorf("this file is not a backup or a config file: %w", err)
	}
	if b.reload != nil {
		if err := b.reload(); err != nil {
			return nil, fmt.Errorf("config imported but reload failed: %w", err)
		}
	}
	n := len(cfg.Companions)
	plural := "s"
	if n == 1 {
		plural = ""
	}
	return &api.ImportResult{
		Kind:       "config",
		Companions: n,
		Detail:     fmt.Sprintf("Imported settings and %d companion%s from a config file.", n, plural),
	}, nil
}
