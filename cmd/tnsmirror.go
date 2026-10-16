package cmd

import (
	"context"
	"log/slog"

	"github.com/USA-RedDragon/astro-stacker/internal/activity"
	"github.com/USA-RedDragon/astro-stacker/internal/config"
	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/discoveryreport"
	"github.com/USA-RedDragon/astro-stacker/internal/starcat"
	"github.com/USA-RedDragon/astro-stacker/internal/tnsmirror"
	"github.com/minio/minio-go/v7"
)

// startTNSMirror keeps the TNS public objects mirror in the survey bucket
// and memory when the TNS bot's credentials are set; nil when it is off.
func startTNSMirror(cfg *config.Config, s3 *minio.Client, task *activity.Task) *tnsmirror.Mirror {
	d := cfg.Discoveries
	c := tnsMirrorClient(d)
	switch {
	case !c.Bot.Enabled():
		slog.Info("TNS mirror off: no TNS bot credentials set (discoveries.tns-key, discoveries.tns-bot-id, discoveries.tns-bot-name)")
		return nil
	case s3 == nil:
		slog.Warn("TNS mirror off: no S3 client")
		return nil
	}
	m := &tnsmirror.Mirror{Bucket: starcat.MinioBucket{Client: s3, Name: cfg.Discover.SurveyBucket}, Prefix: d.TNSMirrorPrefix, Client: c,
		Task: task}
	c.Progress = func(done, total int64) {
		if m.Task.Running() {
			m.Task.SetProgress(done, total)
		}
	}
	go m.Run(context.Background())
	slog.Info("TNS mirror on", "site", c.BaseURL, "bucket", cfg.Discover.SurveyBucket, "prefix", d.TNSMirrorPrefix, "match_arcsec", d.TNSMatchArcsec)
	return m
}

// loadTNSMirror reads the TNS mirror the server keeps in the survey bucket,
// without downloading from TNS, so a one-off discoveries runner matches its
// changes against the local copy instead of the TNS search API; nil when
// there is no copy to read.
func loadTNSMirror(ctx context.Context, cfg *config.Config, s3 *minio.Client) *tnsmirror.Mirror {
	if s3 == nil {
		return nil
	}
	m := &tnsmirror.Mirror{Bucket: starcat.MinioBucket{Client: s3, Name: cfg.Discover.SurveyBucket}, Prefix: cfg.Discoveries.TNSMirrorPrefix}
	if err := m.Load(ctx); err != nil {
		slog.Warn("Could not read the TNS mirror", "error", err)
		return nil
	}
	if m.Index() == nil {
		return nil
	}
	slog.Info("TNS mirror read", "objects", m.Index().Len())
	return m
}

func tnsSite(d config.Discoveries) tnsmirror.Site { return tnsmirror.Site(d.TNSBaseURL) }

func tnsMirrorClient(d config.Discoveries) *tnsmirror.Client {
	return &tnsmirror.Client{Bot: tnsmirror.Bot{ID: d.TNSBotID, Name: d.TNSBotName, Key: d.TNSKey}, BaseURL: tnsSite(d).Files()}
}

func tnsReports(d config.Discoveries) discoveryreport.TNS {
	return discoveryreport.TNS{Key: d.TNSKey, BotID: d.TNSBotID, BotName: d.TNSBotName, BaseURL: tnsSite(d).API()}
}

func tnsCatalogs(d config.Discoveries) *discoveries.Catalogs {
	return &discoveries.Catalogs{TNSKey: d.TNSKey, TNSBotID: d.TNSBotID, TNSBotName: d.TNSBotName, TNSURL: tnsSite(d).API() + "/get/search"}
}
