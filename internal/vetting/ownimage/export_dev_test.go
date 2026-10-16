//go:build vettingdev

package ownimage

import (
	"testing"

	"github.com/minio/minio-go/v7"
)

func NewDevData(t *testing.T) (Data, *minio.Client) {
	t.Helper()
	d := newDev(t)
	return d, d.mc
}
