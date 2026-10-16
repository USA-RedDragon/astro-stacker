package cmd

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/config"
	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/starcat"
	"github.com/USA-RedDragon/astro-stacker/internal/store"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/ownimage"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/surveys"
	configulator "github.com/USA-RedDragon/configulator/v2"
	"github.com/spf13/cobra"
)

func newVetCommand() *cobra.Command {
	var objects, sources []string
	var budget time.Duration
	var spot, nights, group string
	var sign int
	var delta float64
	var prefetch bool
	c := &cobra.Command{
		Use:          "vet",
		Short:        "Check the new discoveries that are due (or --source ids, or one --spot) and store a suggested verdict for each, then exit",
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
			appStore, err := store.NewAppStore(cfg)
			if err != nil {
				return err
			}
			s3, err := newS3(cfg)
			if err != nil {
				return err
			}
			processed := discoveries.MinioStore{Client: s3, Bucket: cfg.S3.ProcessedBucket}
			gaia, vsx := catalogMirrors(cfg, s3)
			mirrors := vetting.Mirrors{VSX: vsx, Variables: map[string]*starcat.Variables{}}
			if cfg.Discover.CatalogMirror {
				b := starcat.MinioBucket{Client: s3, Name: cfg.Discover.SurveyBucket}
				for _, vc := range starcat.VarCatalogs() {
					mirrors.Variables[vc.Name] = starcat.NewVariables(b, vc.Prefix)
				}
			}
			if m := loadTNSMirror(cmd.Context(), cfg, s3); m != nil {
				mirrors.TNS = m.Index
			}
			cat := tnsCatalogs(cfg.Discoveries)
			cat.Local = smallBodies(cfg, appStore)
			prefix := cfg.Vetting.CachePrefix
			fetcher := &surveys.Fetcher{Store: surveys.MinioStore{Client: s3, Bucket: cfg.S3.ProcessedBucket}, Prefix: prefix,
				HTTP: &http.Client{Timeout: 10 * time.Minute}, ATLASToken: cfg.Vetting.ATLASToken, WorkDir: cfg.Vetting.WorkDir, Now: time.Now}
			svc := &vetting.Service{
				App:     appStore.DB(),
				Data:    ownimage.DBData{App: appStore.DB(), Store: processed, Gaia: gaia},
				Gaia:    gaia,
				Mirrors: mirrors,
				Simbad:  vetting.SimbadTAP(nil, "", processed, prefix, time.Now),
				Bodies:  cat.KnownBodies,
				Surveys: fetcher,
				Threads: cfg.Vetting.Threads,
			}
			if prefetch {
				return svc.Prefetch(cmd.Context(), objects)
			}
			if spot != "" {
				return vetSpot(cmd, svc, spot, nights, group, sign, delta)
			}
			ids := make([]int, 0, len(sources))
			for _, s := range sources {
				id, err := strconv.Atoi(s)
				if err != nil {
					return fmt.Errorf("--source must be a number: %w", err)
				}
				ids = append(ids, id)
			}
			st, err := svc.Run(cmd.Context(), vetting.RunOptions{Objects: objects, IDs: ids, Budget: budget})
			slog.Info("Vetting done", "sources", st.Sources, "done", st.Done, "failed", st.Failed)
			return err
		},
	}
	c.Flags().StringSliceVar(&objects, "object", nil, "vet only these targets")
	c.Flags().StringSliceVar(&sources, "source", nil, "vet these source ids now, due or not")
	c.Flags().DurationVar(&budget, "budget", 0, "start no new target after this long; 0 runs until done")
	c.Flags().StringVar(&spot, "spot", "", "vet a position with no stored source, \"object|ra|dec\" in degrees, and print the report")
	c.Flags().StringVar(&nights, "nights", "", "with --spot, the flagged nights, comma separated YYYY-MM-DD")
	c.Flags().StringVar(&group, "group", discoveries.GroupBroadband, "with --spot, the filter group")
	c.Flags().IntVar(&sign, "sign", -1, "with --spot, 1 for brighter, -1 for fainter")
	c.Flags().Float64Var(&delta, "delta-mag", 0, "with --spot, the reported change in magnitudes")
	c.Flags().BoolVar(&prefetch, "prefetch", false, "pull the survey data of the --object targets' fields and exit")
	return c
}

func vetSpot(cmd *cobra.Command, svc *vetting.Service, spot, nights, group string, sign int, delta float64) error {
	parts := strings.Split(spot, "|")
	if len(parts) != 3 {
		return fmt.Errorf("--spot must be \"object|ra|dec\"")
	}
	ra, err1 := strconv.ParseFloat(parts[1], 64)
	dec, err2 := strconv.ParseFloat(parts[2], 64)
	if err1 != nil || err2 != nil {
		return fmt.Errorf("--spot ra and dec must be degrees")
	}
	sp := vetting.Spot{Object: parts[0], RA: ra, Dec: dec, Group: group, Sign: sign, Delta: delta}
	for _, n := range strings.Split(nights, ",") {
		if n = strings.TrimSpace(n); n == "" {
			continue
		}
		t, err := time.Parse(time.DateOnly, n)
		if err != nil {
			return fmt.Errorf("--nights must be YYYY-MM-DD: %w", err)
		}
		sp.Nights = append(sp.Nights, t)
	}
	rep, err := svc.VetSpot(cmd.Context(), sp)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(rep)
}
