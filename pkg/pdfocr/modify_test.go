package pdfocr

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"codeberg.org/go-pdf/fpdf"

	"github.com/gardar/ocrchestra/pkg/hocr"
)

// mixedSizes are page sizes in points, as on a scan that mixes a card, an A4
// sheet, a landscape receipt and a narrow strip.
var mixedSizes = [][2]float64{{241, 156}, {598, 843}, {610, 598}, {601, 284}}

// mixedSizePDF draws one filled rectangle per page, inset 10pt from each edge.
func mixedSizePDF(t *testing.T) []byte {
	t.Helper()
	pdf := fpdf.New("P", "pt", "", "")
	for _, s := range mixedSizes {
		pdf.AddPageFormat("P", fpdf.SizeType{Wd: s[0], Ht: s[1]})
		pdf.Rect(10, 10, s[0]-20, s[1]-20, "F")
	}
	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// pixelPage is an hOCR page for a PDF page rendered at 200 dpi, with one word
// centred on it.
func pixelPage(n int, s [2]float64, text string) hocr.Page {
	w, h := math.Round(s[0]*200/72), math.Round(s[1]*200/72)
	bb := hocr.BoundingBox{X1: w/2 - 100, Y1: h/2 - 20, X2: w/2 + 100, Y2: h/2 + 20}
	return hocr.Page{
		ID: fmt.Sprintf("page_%d", n), PageNumber: n, BBox: hocr.BoundingBox{X2: w, Y2: h},
		Lines: []hocr.Line{{BBox: bb, Words: []hocr.Word{{Text: text, BBox: bb}}}},
	}
}

func TestApplyOCRKeepsMixedPageSizes(t *testing.T) {
	var h hocr.HOCR
	for i, s := range mixedSizes {
		h.Pages = append(h.Pages, pixelPage(i+1, s, fmt.Sprintf("WORD%d", i+1)))
	}
	config := DefaultConfig()
	config.LogWarnings = false
	out, err := ApplyOCR(mixedSizePDF(t), &h, config)
	if err != nil {
		t.Fatal(err)
	}

	got, err := pageGeometries(io.ReadSeeker(bytes.NewReader(out)))
	if err != nil {
		t.Fatal(err)
	}
	for i, s := range mixedSizes {
		if g := got[i+1]; math.Abs(g.w-s[0]) > 0.5 || math.Abs(g.h-s[1]) > 0.5 {
			t.Errorf("page %d is %.1fx%.1f, want %vx%v", i+1, g.w, g.h, s[0], s[1])
		}
	}

	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("pdftotext not installed")
	}
	path := filepath.Join(t.TempDir(), "out.pdf")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
	bbox, err := exec.Command("pdftotext", "-q", "-bbox", path, "-").Output()
	if err != nil {
		t.Fatal(err)
	}
	// Each page's word must sit in the middle of that page, in points.
	word := regexp.MustCompile(`xMin="([\d.]+)" yMin="([\d.]+)" xMax="([\d.]+)" yMax="([\d.]+)">WORD(\d)<`)
	matches := word.FindAllStringSubmatch(string(bbox), -1)
	if len(matches) != len(mixedSizes) {
		t.Fatalf("found %d words, want %d:\n%s", len(matches), len(mixedSizes), bbox)
	}
	for _, m := range matches {
		n, _ := strconv.Atoi(m[5])
		s := mixedSizes[n-1]
		v := make([]float64, 4)
		for i := range v {
			v[i], _ = strconv.ParseFloat(m[i+1], 64)
		}
		cx, cy := (v[0]+v[2])/2, (v[1]+v[3])/2
		if math.Abs(cx-s[0]/2) > 5 || math.Abs(cy-s[1]/2) > 10 {
			t.Errorf("WORD%d centred at %.0f,%.0f, want about %.0f,%.0f", n, cx, cy, s[0]/2, s[1]/2)
		}
	}
}

func TestApplyOCRRejectsTurnedPage(t *testing.T) {
	var h hocr.HOCR
	for i, s := range mixedSizes {
		if i == 1 {
			s = [2]float64{s[1], s[0]} // the OCR engine turned the A4 page upright
		}
		h.Pages = append(h.Pages, pixelPage(i+1, s, "WORD"))
	}
	config := DefaultConfig()
	config.LogWarnings = false
	_, err := ApplyOCR(mixedSizePDF(t), &h, config)
	if !errors.Is(err, ErrPageGeometry) {
		t.Fatalf("err = %v, want ErrPageGeometry", err)
	}
}

func TestPageGeometriesHonourRotate(t *testing.T) {
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 150] /Rotate 90 /Resources << >> /Contents 4 0 R >>",
		"<< /Length 0 >>\nstream\n\nendstream",
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs))
	for i, o := range objs {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)

	got, err := pageGeometries(io.ReadSeeker(bytes.NewReader(b.Bytes())))
	if err != nil {
		t.Fatal(err)
	}
	if g := got[1]; g.w != 150 || g.h != 300 {
		t.Errorf("page 1 is %vx%v, want 150x300", g.w, g.h)
	}
}
