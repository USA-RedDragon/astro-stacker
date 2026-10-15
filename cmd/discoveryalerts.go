package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/config"
	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/discoveryalert"
	"github.com/USA-RedDragon/astro-stacker/internal/discoveryfollowup"
	"github.com/USA-RedDragon/astro-stacker/internal/events"
	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/morningreport"
	"github.com/USA-RedDragon/astro-stacker/internal/planning"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/server"
	"github.com/USA-RedDragon/astro-stacker/internal/store"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	configulator "github.com/USA-RedDragon/configulator/v2"
	"github.com/spf13/cobra"
)

// schedCommands sends follow-up commands through the scheduler service.
type schedCommands struct{ s *schedcmd.Service }

func (c schedCommands) SubmitOnce(ctx context.Context, id string, kind schedcmd.Kind, payload json.RawMessage, author string) (schedcmd.Record, error) {
	return c.s.SubmitOnce(ctx, id, kind, payload, author)
}

func (c schedCommands) Status(ctx context.Context, id string) (schedcmd.Record, error) {
	return c.s.Log.Get(ctx, id)
}

// cutoutStore reads discovery cutouts from the processed bucket.
type cutoutStore struct{ store discoveries.ObjectStore }

func (c cutoutStore) Cutout(ctx context.Context, key string) ([]byte, error) {
	if c.store == nil {
		return nil, fmt.Errorf("no object store")
	}
	return c.store.Get(ctx, key)
}

func newCutoutStore(cfg *config.Config) cutoutStore {
	if cfg.S3.AccessKey == "" {
		return cutoutStore{}
	}
	s3, err := newS3(cfg)
	if err != nil {
		slog.Warn("Discovery alerts send placeholder pictures; no S3 client", "error", err)
		return cutoutStore{}
	}
	return cutoutStore{store: discoveries.MinioStore{Client: s3, Bucket: cfg.S3.ProcessedBucket}}
}

func alertLoc(cfg *config.Config) *time.Location {
	loc, err := time.LoadLocation(cfg.MorningReport.Timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}

func newFollowups(cfg *config.Config, appStore, schedStore store.Store, commands *schedcmd.Service, backfill func() goals.BackfillLive) *discoveryfollowup.Service {
	f := &discoveryfollowup.Service{DB: appStore.DB(), Loc: alertLoc(cfg), Interval: time.Duration(cfg.DiscoveryAlerts.IntervalMinutes) * time.Minute,
		Snapshot: func(ctx context.Context) (*planning.Snapshot, error) {
			return server.LoadPlanningSnapshot(ctx, appStore.DB(), schedStore.DB(), backfill)
		}}
	if commands != nil {
		f.Commands = schedCommands{s: commands}
	}
	return f
}

// startDiscoveryAlerts sends the alerts on new finds and runs their
// follow-ups, when they are on and Telegram is set up.
func startDiscoveryAlerts(ctx context.Context, cfg *config.Config, appStore, schedStore store.Store, broker *events.Broker, commands *schedcmd.Service,
	backfill func() goals.BackfillLive) {
	a := cfg.DiscoveryAlerts
	if !a.Enabled {
		return
	}
	if a.TelegramToken == "" || a.TelegramChatID == "" {
		slog.Info("Discovery alerts are off: no Telegram token or chat of their own is set")
		return
	}
	svc := &discoveryalert.Service{DB: appStore.DB(), Sender: morningreport.Telegram{Token: a.TelegramToken, ChatID: a.TelegramChatID},
		Cutouts: newCutoutStore(cfg), WebURL: cfg.HTTP.WebURL, Loc: alertLoc(cfg), RetryAfter: 5 * time.Minute, MaxAttempts: 8,
		Interval: time.Duration(a.IntervalMinutes) * time.Minute}
	var follow *discoveryfollowup.Service
	if a.Followup {
		follow = newFollowups(cfg, appStore, schedStore, commands, backfill)
		svc.Followups = follow
		go follow.Run(ctx)
	}
	broker.Listen(func(e events.Event) {
		if e.Type != events.TypeDiscovery {
			return
		}
		svc.Kick()
		if follow != nil {
			follow.Kick()
		}
	})
	go svc.Run(ctx)
	slog.Info("Discovery alerts started", "followup", a.Followup, "backstop_minutes", a.IntervalMinutes)
}

// newDiscoveryAlertCommand renders the alert one candidate would send, and
// the follow-up project it would ask for, into a folder without sending
// or storing anything.
func newDiscoveryAlertCommand() *cobra.Command {
	var id int
	var out string
	var asNew bool
	c := &cobra.Command{
		Use:          "discovery-alert",
		Short:        "Render the Telegram alert and the follow-up project for one discovery candidate into a folder; nothing is sent and the databases are only read",
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
			return renderAlert(cmd.Context(), cfg, appStore, schedStore, id, asNew, out)
		},
	}
	c.Flags().IntVar(&id, "candidate", 0, "the discovery candidate's id")
	c.Flags().BoolVar(&asNew, "as-new", false, "render it even when it would not alert: a known class, a failed catalogue check or an earlier alert")
	c.Flags().StringVar(&out, "out", "discovery-alert", "folder the message, picture and follow-up project are written to")
	return c
}

func renderAlert(ctx context.Context, cfg *config.Config, appStore, schedStore store.Store, id int, asNew bool, out string) error {
	db := appStore.DB().WithContext(ctx)
	var c app.DiscoveryCandidate
	if err := db.First(&c, id).Error; err != nil {
		return fmt.Errorf("candidate %d: %w", id, err)
	}
	var run app.DiscoveryRun
	if err := db.First(&run, c.RunID).Error; err != nil {
		return fmt.Errorf("run %d: %w", c.RunID, err)
	}
	why := []string{}
	if !discoveryalert.NewClass(c.Class) {
		why = append(why, "class "+c.Class+" is not new")
	}
	if !discoveryalert.ChecksPassed(run, c.Kind) {
		why = append(why, fmt.Sprintf("catalogue checks: checked %q, failed %q, needs %s", run.Checked, run.Failed,
			strings.Join(discoveryalert.RequiredChecks(c.Kind), ", ")))
	}
	alertable := discoveryalert.Alertable(c, run)
	if !alertable && !asNew {
		return fmt.Errorf("candidate %d would not alert (%s); pass --as-new to render it anyway", id, strings.Join(why, "; "))
	}
	svc := &discoveryalert.Service{Cutouts: newCutoutStore(cfg), WebURL: cfg.HTTP.WebURL}
	msg, rows := svc.Render(ctx, discoveryalert.Batch{RunID: run.ID, Cands: []app.DiscoveryCandidate{c}})
	if err := os.MkdirAll(out, 0o750); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "message.html"), []byte(msg.Text+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "message.txt"), []byte(morningreport.PlainText(msg.Text)+"\n"), 0o600); err != nil {
		return err
	}
	if len(rows) == 1 && len(rows[0].Photo) > 0 {
		if err := os.WriteFile(filepath.Join(out, "photo.jpg"), rows[0].Photo, 0o600); err != nil {
			return err
		}
	}
	loc := alertLoc(cfg)
	now := time.Now()
	f := app.DiscoveryFollowup{Object: c.Object, Group: c.Group, Night: c.Night, Kind: c.Kind, Class: c.Class, Sign: c.Sign, RA: c.RA, Dec: c.Dec,
		ProjectGUID: discoveryfollowup.ProjectGUID(c.Object, c.Group, c.Night), Until: discoveryfollowup.Tonight(now, loc).AddDate(0, 0, discoveryfollowup.Nights-1)}
	follow := newFollowups(cfg, appStore, schedStore, nil, nil)
	snap, err := follow.Snapshot(ctx)
	var note string
	var payload any
	if err != nil {
		note = "planning snapshot: " + err.Error()
	} else {
		orig, err := discoveryfollowup.OriginalTarget(ctx, appStore.DB(), &f)
		if err != nil {
			return err
		}
		p, pick, why := discoveryfollowup.Draft(snap, &f, orig, now, loc)
		note = why
		if p != nil {
			payload = map[string]any{"kind": schedcmd.KindProjectCreate, "pick": pick, "payload": p}
		}
	}
	if payload == nil {
		payload = map[string]any{"not_sent": note}
	}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "followup.json"), append(raw, '\n'), 0o600); err != nil {
		return err
	}
	state := "would alert"
	if !alertable {
		state = "would not alert (" + strings.Join(why, "; ") + "), rendered as if new"
	}
	fmt.Printf("candidate %d: %s; message, picture and follow-up written to %s\n", id, state, out)
	return nil
}
