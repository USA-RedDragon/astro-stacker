// Package astrometry plate solves images with a local astrometry.net
// (solve-field) and keeps its index files.
package astrometry

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// indexList is the index files solve-field searches, one per line: SHA-256,
// size in bytes and where to download it. They hold quads of 11'-22' (the
// 5200 LITE series, Gaia DR2 + Tycho-2, scales 5205 and 5206) and 30'-8°
// (Tycho-2 scales 4108 to 4115): fields from Telescope.live's 22' SPA-2 to
// its 5.4° refractors and the observatory's 3.3° x 2.2°. 4107 (22'-30',
// 165 MB) is left out; the 5200 scales solve 22'-32' fields. The 4100 hashes were taken from files
// that matched data.astrometry.net's published md5sums.txt; the 5200 LITE
// set publishes no checksums, so its hashes pin the files first downloaded.
//
//go:embed index.txt
var indexList []byte

// IndexFile is one index file solve-field searches.
type IndexFile struct {
	Name   string
	URL    string
	Size   int64
	SHA256 string
}

// IndexFiles lists the index files.
func IndexFiles() ([]IndexFile, error) {
	var out []IndexFile
	sc := bufio.NewScanner(bytes.NewReader(indexList))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 {
			continue
		}
		if len(f) != 3 {
			return nil, fmt.Errorf("index list: bad line %q", sc.Text())
		}
		size, err := strconv.ParseInt(f[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("index list: %w", err)
		}
		out = append(out, IndexFile{Name: path.Base(f[2]), URL: f[2], Size: size, SHA256: f[0]})
	}
	return out, sc.Err()
}

// IndexSetID names the index set, so a plate solve that failed with one set
// is tried again with another.
func IndexSetID() string {
	sum := sha256.Sum256(indexList)
	return hex.EncodeToString(sum[:6])
}

// EnsureIndex makes dir hold every index file, each checked against its
// size and SHA-256. Files that are missing or don't match are downloaded
// again; a download is kept only when it matches.
func EnsureIndex(ctx context.Context, client *http.Client, dir string) error {
	files, err := IndexFiles()
	if err != nil {
		return err
	}
	return ensureFiles(ctx, client, dir, files)
}

func ensureFiles(ctx context.Context, client *http.Client, dir string, files []IndexFile) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	fetched := 0
	for _, f := range files {
		name := filepath.Join(dir, f.Name)
		if err := verify(name, f); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("Astrometry index file does not match its checksum; downloading it again", "file", f.Name, "error", err)
		}
		var err error
		for range downloadTries {
			if err = downloadWithin(ctx, client, name, f); err == nil || ctx.Err() != nil {
				break
			}
			slog.Warn("Astrometry index download failed", "file", f.Name, "error", err)
		}
		if err != nil {
			return fmt.Errorf("download %s: %w", f.Name, err)
		}
		fetched++
	}
	if fetched > 0 {
		slog.Info("Downloaded astrometry index files", "files", fetched, "of", len(files))
	}
	return nil
}

func verify(name string, f IndexFile) error {
	st, err := os.Stat(name)
	if err != nil {
		return err
	}
	if st.Size() != f.Size {
		return fmt.Errorf("size %d, want %d", st.Size(), f.Size)
	}
	got, err := fileSHA256(name)
	if err != nil {
		return err
	}
	if got != f.SHA256 {
		return fmt.Errorf("sha256 %s, want %s", got, f.SHA256)
	}
	return nil
}

func fileSHA256(name string) (string, error) {
	r, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer r.Close()
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// downloadTries is how many times a file is downloaded before giving up,
// each within downloadTime: index files are at most 95 MB.
const (
	downloadTries = 3
	downloadTime  = 10 * time.Minute
)

func downloadWithin(ctx context.Context, client *http.Client, name string, f IndexFile) error {
	ctx, cancel := context.WithTimeout(ctx, downloadTime)
	defer cancel()
	return download(ctx, client, name, f)
}

func download(ctx context.Context, client *http.Client, name string, f IndexFile) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.URL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", f.URL, resp.Status)
	}
	part := name + ".part"
	out, err := os.Create(part)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(resp.Body, f.Size+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && n != f.Size {
		err = fmt.Errorf("got %d bytes, want %d", n, f.Size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); err == nil && got != f.SHA256 {
		err = fmt.Errorf("sha256 %s, want %s", got, f.SHA256)
	}
	if err != nil {
		_ = os.Remove(part)
		return err
	}
	return os.Rename(part, name)
}
