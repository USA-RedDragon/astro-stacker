package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/previewer"
	"github.com/USA-RedDragon/astro-stacker/internal/stacking"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func TestStackMasterLocalNorm(t *testing.T) {
	t.Parallel()
	mc, err := minio.New("127.0.0.1:1", &minio.Options{Creds: credentials.NewStaticV4("k", "s", ""), Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	signer := previewer.NewSigner(mc, mc, "processed", time.Hour)
	key := "stacks/M31/Red/master.fit"
	subs, uneven := 7, 0.23
	for _, tc := range []struct {
		name  string
		stack app.Stack
		want  string
	}{
		{"ran", app.Stack{LocalNormSubs: &subs, LocalNormUneven: &uneven},
			`{"local_norm":true,"local_norm_subs":7,"local_norm_threshold":0.1,"local_norm_uneven":0.23}`},
		{"not run", app.Stack{LocalNormSubs: new(int), LocalNormUneven: new(float64)},
			`{"local_norm":false,"local_norm_subs":0,"local_norm_threshold":0.1,"local_norm_uneven":0}`},
		{"unknown", app.Stack{},
			`{"local_norm":null,"local_norm_subs":null,"local_norm_threshold":0.1,"local_norm_uneven":null}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := tc.stack
			s.Object, s.Filter, s.MasterKey = objectM31, filterRed, &key
			m, err := stackMaster(context.Background(), signer, s)
			if err != nil {
				t.Fatal(err)
			}
			full, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			var all map[string]json.RawMessage
			if err := json.Unmarshal(full, &all); err != nil {
				t.Fatal(err)
			}
			b, err := json.Marshal(map[string]json.RawMessage{
				"local_norm": all["local_norm"], "local_norm_subs": all["local_norm_subs"],
				"local_norm_uneven": all["local_norm_uneven"], "local_norm_threshold": all["local_norm_threshold"],
			})
			if err != nil {
				t.Fatal(err)
			}
			if string(b) != tc.want {
				t.Errorf("got %s, want %s", b, tc.want)
			}
		})
	}
	if stacking.LocalNormThreshold != 0.1 {
		t.Errorf("threshold = %v", stacking.LocalNormThreshold)
	}
}
