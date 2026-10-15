package discoveryreport

import (
	"encoding/xml"
	"fmt"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
)

// Observer is who and what the reports name.
type Observer struct {
	// MPCCode is the Minor Planet Center observatory code; empty when not
	// set.
	MPCCode   string
	Reporter  string
	Telescope string
	Detector  string
}

func adesNum(v float64, prec int) string { return fmt.Sprintf("%.*f", prec, v) }

// ADESPSV is a moving object's detections in the ADES pipe-separated
// format the Minor Planet Center takes. stn is the observatory code.
func ADESPSV(t discoveries.TrackRecord, o Observer) string {
	var b strings.Builder
	b.WriteString("# version=2017\n# observatory\n! mpcCode " + o.MPCCode + "\n")
	if o.Reporter != "" {
		b.WriteString("# submitter\n! name " + o.Reporter + "\n# observers\n! name " + o.Reporter + "\n# measurers\n! name " + o.Reporter + "\n")
	}
	if o.Telescope != "" || o.Detector != "" {
		b.WriteString("# telescope\n")
		if o.Telescope != "" {
			b.WriteString("! design " + o.Telescope + "\n")
		}
		b.WriteString("! detector CCD\n")
	}
	b.WriteString("trkSub  |mode|stn |obsTime                 |ra         |dec        |rmsRA |rmsDec|astCat|mag  |rmsMag|band|photCat\n")
	for _, a := range t.ADES {
		mag, rms, band, phot := "", "", "", ""
		if a.Mag != nil {
			mag, band, phot = adesNum(*a.Mag, 2), a.Band, a.PhotCat
			if a.RMSMag != nil {
				rms = adesNum(*a.RMSMag, 2)
			}
		}
		fmt.Fprintf(&b, "%-8s|%-4s|%-4s|%-24s|%-11s|%-11s|%-6s|%-6s|%-6s|%-5s|%-6s|%-4s|%s\n", t.TrkSub, a.Mode, o.MPCCode, a.ObsTime,
			adesNum(a.RA, 6), signed(a.Dec, 6), adesNum(a.RMSRA, 3), adesNum(a.RMSDec, 3), a.AstCat, mag, rms, band, phot)
	}
	return b.String()
}

func signed(v float64, prec int) string {
	s := adesNum(v, prec)
	if !strings.HasPrefix(s, "-") {
		s = "+" + s
	}
	return s
}

type xmlOptical struct {
	TrkSub  string `xml:"trkSub"`
	Mode    string `xml:"mode"`
	Stn     string `xml:"stn"`
	ObsTime string `xml:"obsTime"`
	RA      string `xml:"ra"`
	Dec     string `xml:"dec"`
	RMSRA   string `xml:"rmsRA"`
	RMSDec  string `xml:"rmsDec"`
	AstCat  string `xml:"astCat"`
	Mag     string `xml:"mag,omitempty"`
	RMSMag  string `xml:"rmsMag,omitempty"`
	Band    string `xml:"band,omitempty"`
	PhotCat string `xml:"photCat,omitempty"`
}

type xmlName struct {
	Name string `xml:"name"`
}

type xmlContext struct {
	Observatory struct {
		MPCCode string `xml:"mpcCode"`
	} `xml:"observatory"`
	Submitter *xmlName `xml:"submitter,omitempty"`
	Observers *xmlName `xml:"observers,omitempty"`
	Measurers *xmlName `xml:"measurers,omitempty"`
	Telescope *struct {
		Design   string `xml:"design,omitempty"`
		Detector string `xml:"detector"`
	} `xml:"telescope,omitempty"`
}

type xmlADES struct {
	XMLName  xml.Name `xml:"ades"`
	Version  string   `xml:"version,attr"`
	ObsBlock struct {
		Context xmlContext `xml:"obsContext"`
		Data    struct {
			Optical []xmlOptical `xml:"optical"`
		} `xml:"obsData"`
	} `xml:"obsBlock"`
}

// ADESXML is the same detections in ADES XML.
func ADESXML(t discoveries.TrackRecord, o Observer) (string, error) {
	var doc xmlADES
	doc.Version = "2017"
	ctx := &doc.ObsBlock.Context
	ctx.Observatory.MPCCode = o.MPCCode
	if o.Reporter != "" {
		ctx.Submitter, ctx.Observers, ctx.Measurers = &xmlName{o.Reporter}, &xmlName{o.Reporter}, &xmlName{o.Reporter}
	}
	if o.Telescope != "" || o.Detector != "" {
		ctx.Telescope = &struct {
			Design   string `xml:"design,omitempty"`
			Detector string `xml:"detector"`
		}{Design: o.Telescope, Detector: "CCD"}
	}
	for _, a := range t.ADES {
		x := xmlOptical{TrkSub: t.TrkSub, Mode: a.Mode, Stn: o.MPCCode, ObsTime: a.ObsTime, RA: adesNum(a.RA, 6), Dec: signed(a.Dec, 6),
			RMSRA: adesNum(a.RMSRA, 3), RMSDec: adesNum(a.RMSDec, 3), AstCat: a.AstCat}
		if a.Mag != nil {
			x.Mag, x.Band, x.PhotCat = adesNum(*a.Mag, 2), a.Band, a.PhotCat
			if a.RMSMag != nil {
				x.RMSMag = adesNum(*a.RMSMag, 2)
			}
		}
		doc.ObsBlock.Data.Optical = append(doc.ObsBlock.Data.Optical, x)
	}
	out, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	return xml.Header + string(out) + "\n", nil
}
