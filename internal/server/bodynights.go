package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/previewer"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/store/tables"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type BodyNightImage struct {
	Filter      string    `json:"filter"`
	Night       string    `json:"night"`
	Subs        int       `json:"subs"`
	PreviewURL  string    `json:"previewUrl,omitempty"`
	MasterURL   string    `json:"masterUrl,omitempty"`
	StarlessURL string    `json:"starlessUrl,omitempty"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

func bodyNightsRoute(signer *previewer.Signer) gin.HandlerFunc {
	return func(c *gin.Context) {
		objects := c.QueryArray("object")
		if len(objects) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{errorKey: objectRequired})
			return
		}
		di, ok := depInjection(c)
		if !ok {
			return
		}
		out, err := bodyNightImages(c.Request.Context(), di.AppStore.DB(), signer, objects)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{errorKey: err.Error()})
			return
		}
		c.JSON(http.StatusOK, out)
	}
}

func bodyNightImages(ctx context.Context, db *gorm.DB, signer *previewer.Signer, objects []string) ([]BodyNightImage, error) {
	out := []BodyNightImage{}
	if has, err := tables.Has(ctx, db, &app.BodyNight{}); err != nil || !has {
		return out, err
	}
	var rows []app.BodyNight
	if err := db.WithContext(ctx).Where("object IN ? AND preview_key IS NOT NULL", objects).Order("night DESC, filter").Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		night := r.Night.UTC().Format(goals.NightLayout)
		img := BodyNightImage{Filter: r.Filter, Night: night, Subs: r.Subs, UpdatedAt: r.UpdatedAt}
		if signer != nil {
			base := fmt.Sprintf("%s_%s_%s_", strings.ReplaceAll(r.Object, " ", "_"), strings.ReplaceAll(r.Filter, " ", "_"), night)
			var err error
			if img.PreviewURL, err = signer.URL(ctx, *r.PreviewKey); err != nil {
				return nil, err
			}
			if r.MasterKey != nil {
				if img.MasterURL, err = signer.DownloadURL(ctx, *r.MasterKey, base+"body.fit"); err != nil {
					return nil, err
				}
			}
			if r.StarlessKey != nil {
				if img.StarlessURL, err = signer.DownloadURL(ctx, *r.StarlessKey, base+"body-starless.fit"); err != nil {
					return nil, err
				}
			}
		}
		out = append(out, img)
	}
	return out, nil
}
