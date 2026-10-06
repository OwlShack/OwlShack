package trigger

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/OwlShack/OwlShack/internal/config"
	"github.com/OwlShack/OwlShack/internal/logging"
	"github.com/robfig/cron/v3"
)

type CronTrigger struct {
	cfg      config.TriggerConfig
	botName  string
	cron     *cron.Cron
	log      *slog.Logger
	mu       sync.Mutex
	callback Callback
}

func NewCronTrigger(botName string, cfg config.TriggerConfig, log *slog.Logger) (*CronTrigger, error) {
	if cfg.Schedule == "" {
		return nil, fmt.Errorf("cron trigger requires a schedule")
	}
	return &CronTrigger{
		cfg:     cfg,
		botName: botName,
		log:     log.With("trigger", "cron", "schedule", cfg.Schedule),
	}, nil
}

func (t *CronTrigger) Start(ctx context.Context, callback Callback) error {
	t.mu.Lock()
	t.callback = callback
	t.cron = cron.New()
	t.mu.Unlock()

	_, err := t.cron.AddFunc(t.cfg.Schedule, func() {
		select {
		case <-ctx.Done():
			return
		default:
		}

		t.log.Log(ctx, logging.LevelTrace, "cron fired")

		t.mu.Lock()
		cb := t.callback
		t.mu.Unlock()
		if cb == nil {
			return
		}

		cb(Event{
			Type:    "cron",
			BotName: t.botName,
			Data: map[string]any{
				"Time":     time.Now(),
				"Schedule": t.cfg.Schedule,
			},
		})
	})
	if err != nil {
		return fmt.Errorf("invalid cron schedule %q: %w", t.cfg.Schedule, err)
	}

	t.cron.Start()

	go func() {
		<-ctx.Done()
		t.Stop()
	}()

	return nil
}

func (t *CronTrigger) Stop() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cron != nil {
		t.cron.Stop()
		t.cron = nil
	}
	return nil
}

var _ Trigger = (*CronTrigger)(nil)
