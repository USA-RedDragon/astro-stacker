package tnsmirror

import (
	"context"
	"time"
)

func (m *Mirror) Step(ctx context.Context) time.Time { return m.step(ctx) }
