package stacking

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/indexer"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/minio/minio-go/v7"
)

// Registration against the optics' distortion. Telescope.live's subs of a
// target are pointed up to 1.7° apart on a 5.4° field, and Siril's
// star-matching homography can't take up the difference in distortion
// across such a shift: the far subs of Rho Ophiuchi landed 3 to 5 pixels
// off at the edges, and stars doubled in the colour cover. Registered
// with the distortion of the optics (Siril's register -disto), the stars
// are matched and the subs resampled undistorted, so a homography fits;
// the same far subs land within 1 to 2 pixels.
//
// The distortion comes from Siril's own plate solve of the reference, with
// SIP terms of distortionOrder. Telescope.live's subs carry SIP solutions
// of their own, but they are fitted sub by sub and disagree by over 10
// pixels at the corners for the same pointing: registering by them
// (seqplatesolve then seqapplyreg) put subs up to 28 pixels off.
//
// A reference with a distortion solution is resampled undistorted too, so
// its subs share its pixel grid undistorted rather than as it was shot. It
// is used for subs from the reference's camera that come calibrated; our
// own subs are registered by stars as they always were.

// How subs are registered to a target's reference (TargetReference.
// Registration).
const (
	registrationStars      = "stars"
	registrationDistortion = "distortion"
)

// distortionOrder is the order of the SIP polynomials the reference is
// solved with. Orders 3 and 5 registered Rho Ophiuchi's far subs alike.
const distortionOrder = 3

// solveReference plate solves a calibrated reference with distortion terms
// from where the light f pointed and its optics, and returns the solved
// file beside it.
func (p *Pipeline) solveReference(ctx context.Context, dir, file string, f app.Frame) (string, error) {
	kw, err := indexer.ReadHeader(ctx, p.s3, p.source, minio.ObjectInfo{Key: f.Key, Size: f.Size})
	if err != nil {
		return "", fmt.Errorf("read %s: %w", f.Key, err)
	}
	h := frameheader.FromKeywords(kw)
	if math.IsNaN(h.RA) || math.IsNaN(h.Dec) {
		return "", fmt.Errorf("%s has no pointing", f.Key)
	}
	focal, pixel := kw.Float("FOCALLEN"), kw.Float("XPIXSZ")
	if bin := kw.Float("XBINNING"); bin > 1 {
		pixel *= bin
	}
	if !(focal > 0) || !(pixel > 0) {
		focal, pixel = solutionOptics(kw, pixel)
	}
	if !(focal > 0) || !(pixel > 0) {
		return "", fmt.Errorf("%s has no optics", f.Key)
	}
	name := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	var runErr error
	for _, extra := range []string{"", " -downscale"} {
		// -noflip: the reference keeps its pixel grid whatever its
		// parity; subs are registered to it as it is.
		script := p.sirilPreamble(true) + fmt.Sprintf("load %s\nplatesolve %.6f,%.6f -focal=%.2f -pixelsize=%.3f -force -noflip -order=%d%s\nsave ref_solved\n",
			name, h.RA, h.Dec, focal, pixel, distortionOrder, extra)
		if _, runErr = p.siril.Run(ctx, filepath.Dir(file), script); runErr == nil {
			break
		}
	}
	if runErr != nil {
		return "", fmt.Errorf("plate solve: %w", runErr)
	}
	solved := filepath.Join(filepath.Dir(file), "ref_solved.fit")
	if !hasDistortion(solved) {
		return "", fmt.Errorf("the solution has no distortion terms")
	}
	return solved, nil
}

// hasDistortion reports whether a FITS file's plate solution has SIP
// distortion terms beyond the linear, as register -disto needs.
func hasDistortion(file string) bool {
	f, err := os.Open(file)
	if err != nil {
		return false
	}
	defer f.Close()
	b := make([]byte, 64*2880)
	n, _ := io.ReadFull(f, b)
	cards, err := frameheader.ParseCards(b[:n])
	if err != nil {
		return false
	}
	for _, c := range cards {
		if c.Name == "A_ORDER" {
			order, err := strconv.Atoi(strings.TrimSpace(c.Value))
			return err == nil && order >= 2
		}
	}
	return false
}

// registerWithDistortion reports whether a batch is registered with the
// reference's distortion: the reference has it, and every sub came
// calibrated from the reference's camera, so shares its optics.
func registerWithDistortion(tr app.TargetReference, ref app.Frame, batch []candidate) bool {
	if tr.Registration != registrationDistortion {
		return false
	}
	for _, c := range batch {
		if !precalibrated(c.frame) || c.frame.Camera != ref.Camera {
			return false
		}
	}
	return true
}

// registerCommand registers the sequence seq_ to its first image, the
// reference, with the distortion in the reference's plate solution when
// disto.
func registerCommand(disto bool) string {
	if disto {
		return "register seq_ -disto=file seq_00001.fit -prefix=r_\n"
	}
	return "register seq_ -prefix=r_\n"
}

// reregisterPrecalibrated restacks targets whose reference is a sub that
// came calibrated but was chosen before registration could use the
// distortion, so their subs are registered again with it (and a reference
// is chosen from the pointing their plate solutions now give).
func (p *Pipeline) reregisterPrecalibrated(ctx context.Context) {
	var refs []struct {
		Object string
		Key    string
	}
	if err := p.db.WithContext(ctx).Table("target_references tr").Select("tr.object, f.key").
		Joins("JOIN frames f ON f.id = tr.frame_id").Where("tr.registration = ''").Scan(&refs).Error; err != nil {
		slog.Warn("Could not find references to register again", "error", err)
		return
	}
	for _, r := range refs {
		if p.stopping(ctx) {
			return
		}
		if !precalibrated(app.Frame{Key: r.Key}) || !p.hold(r.Object) {
			continue
		}
		slog.Warn("Restacking target to register its calibrated subs with the optics' distortion", "object", r.Object)
		err := p.restack(ctx, r.Object, nil)
		p.release(r.Object)
		if err != nil {
			slog.Error("Restacking target failed", "object", r.Object, "error", err)
		}
	}
}
