package cmd

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/config"
	"github.com/USA-RedDragon/astro-stacker/internal/morningreport"
	"github.com/USA-RedDragon/astro-stacker/internal/store"
	configulator "github.com/USA-RedDragon/configulator/v2"
	"github.com/minio/minio-go/v7"
	"github.com/spf13/cobra"
)

type bucketPreviews struct {
	s3     *minio.Client
	bucket string
}

func (b bucketPreviews) Get(ctx context.Context, key string) ([]byte, error) {
	obj, err := b.s3.GetObject(ctx, b.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer obj.Close()
	return io.ReadAll(io.LimitReader(obj, 64<<20))
}

// newMorningReport builds the report service from the config; its Sender is
// nil unless send is set.
func newMorningReport(cfg *config.Config, appStore, schedStore store.Store, send bool) (*morningreport.Service, error) {
	m := cfg.MorningReport
	hour, minute, err := m.Clock()
	if err != nil {
		return nil, err
	}
	loc, err := time.LoadLocation(m.Timezone)
	if err != nil {
		return nil, err
	}
	sched := morningreport.Schedule{Loc: loc, Hour: hour, Minute: minute}
	c := morningreport.Collector{
		App:      appStore.DB(),
		Sched:    schedStore.DB(),
		Metrics:  morningreport.Metrics{URL: cfg.Scheduler.MetricsURL, Client: &http.Client{Timeout: 30 * time.Second}},
		Logs:     morningreport.Logs{URL: m.LokiURL, Client: &http.Client{Timeout: 2 * time.Minute}},
		Schedule: sched,
		Site:     siteFunc(cfg, appStore),
	}
	if cfg.S3.AccessKey != "" {
		s3, err := newS3(cfg)
		if err != nil {
			return nil, err
		}
		c.Previews = bucketPreviews{s3: s3, bucket: cfg.S3.ProcessedBucket}
	}
	svc := &morningreport.Service{
		DB:          appStore.DB(),
		Collector:   c,
		Formatter:   morningreport.Formatter{Loc: loc, Zone: m.ZoneLabel},
		Schedule:    sched,
		RetryAfter:  15 * time.Minute,
		MaxAttempts: 4,
		First:       m.FirstNightDate(),
	}
	if send {
		svc.Sender = morningreport.Telegram{Token: m.TelegramToken, ChatID: m.TelegramChatID}
	}
	return svc, nil
}

// startMorningReport runs the daily report when it is enabled.
func startMorningReport(ctx context.Context, cfg *config.Config, appStore, schedStore store.Store) {
	if !cfg.MorningReport.Enabled {
		return
	}
	report, err := newMorningReport(cfg, appStore, schedStore, true)
	if err != nil {
		slog.Error("Morning report not started", "error", err)
		return
	}
	go report.Run(ctx)
	slog.Info("Morning report started", "time", cfg.MorningReport.Time, "timezone", cfg.MorningReport.Timezone,
		"next", report.Schedule.Next(time.Now()).Format(time.RFC3339))
}

// newMorningReportCommand renders a night's morning report into a folder
// without sending it.
func newMorningReportCommand() *cobra.Command {
	var night, out string
	c := &cobra.Command{
		Use:          "morning-report",
		Short:        "Render a night's morning report and its photos into a folder; nothing is sent and the databases are only read",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cl, err := configulator.FromContext[config.Config](cmd.Context())
			if err != nil {
				return fmt.Errorf("failed to get config from context")
			}
			cfg, err := cl.Load()
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}
			appStore, err := store.OpenReadOnly(cfg.Storage.Type, cfg.Storage.DSN.App)
			if err != nil {
				return err
			}
			schedType := cfg.Storage.Type
			if cfg.Storage.SchedulerDBType != "" {
				schedType = cfg.Storage.SchedulerDBType
			}
			schedStore, err := store.OpenReadOnly(schedType, cfg.Storage.DSN.SchedulerDB)
			if err != nil {
				return err
			}
			svc, err := newMorningReport(cfg, appStore, schedStore, false)
			if err != nil {
				return err
			}
			n, _ := svc.Schedule.Due(time.Now())
			if night != "" {
				if n, err = time.Parse(time.DateOnly, night); err != nil {
					return fmt.Errorf("--night must be YYYY-MM-DD: %w", err)
				}
			}
			text, photos, _, err := svc.Render(cmd.Context(), n)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(out, 0o750); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(out, "message.html"), []byte(text+"\n"), 0o600); err != nil {
				return err
			}
			captions := ""
			for i, p := range photos {
				name := fmt.Sprintf("photo-%02d.jpg", i+1)
				if err := os.WriteFile(filepath.Join(out, name), p.JPEG, 0o600); err != nil {
					return err
				}
				captions += fmt.Sprintf("%s (%d bytes): %s\n", name, len(p.JPEG), p.Caption)
			}
			if err := os.WriteFile(filepath.Join(out, "captions.txt"), []byte(captions), 0o600); err != nil {
				return err
			}
			fmt.Printf("night %s: %d chars, %d photos written to %s\n", morningreport.NightKey(n), len(text), len(photos), out)
			return nil
		},
	}
	c.Flags().StringVar(&night, "night", "", "the night's evening date, YYYY-MM-DD; default the night whose report came due last")
	c.Flags().StringVar(&out, "out", "morning-report", "folder the message and photos are written to")
	return c
}
