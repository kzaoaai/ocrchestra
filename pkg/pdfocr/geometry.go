package pdfocr

import (
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/phpdave11/gofpdi"
)

// ErrPageGeometry reports an hOCR page whose shape does not match the PDF page
// it belongs to, so its text cannot be placed on that page. The usual cause is
// an OCR engine that turned a sideways scan upright before reading it.
var ErrPageGeometry = errors.New("hOCR page shape does not match the PDF page")

// aspectTolerance is how far, relatively, an hOCR page's width-to-height
// ratio may stray from its PDF page's before the two are taken to differ.
// Rendering rounds each side to whole pixels, which moves the ratio of even a
// small page by well under 1%.
const aspectTolerance = 0.03

// pageGeometry is a PDF page's size as displayed, in points: its MediaBox,
// with width and height swapped when /Rotate turns it by 90 or 270 degrees.
type pageGeometry struct {
	w, h float64
}

// pageGeometries reads every page's displayed size, keyed by 1-based page
// number, and rewinds rs.
func pageGeometries(rs io.ReadSeeker) (result map[int]pageGeometry, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("reading page sizes: %v", r)
		}
	}()
	defer func() {
		if _, serr := rs.Seek(0, io.SeekStart); serr != nil && err == nil {
			err = serr
		}
	}()

	imp := gofpdi.NewImporter()
	imp.SetSourceStream(&rs)
	sizes := imp.GetPageSizes()
	rotations := imp.GetPageRotations()

	result = make(map[int]pageGeometry, len(sizes))
	for n, boxes := range sizes {
		box, ok := boxes["/MediaBox"]
		if !ok {
			return nil, fmt.Errorf("page %d has no MediaBox", n)
		}
		g := pageGeometry{w: box["w"], h: box["h"]}
		if rotations[n]%180 == 90 {
			g.w, g.h = g.h, g.w
		}
		result[n] = g
	}
	return result, nil
}

// checkAspect reports ErrPageGeometry when an hOCR page of hocrW x hocrH does
// not have this page's shape.
func (g pageGeometry) checkAspect(hocrW, hocrH float64) error {
	if hocrW <= 0 || hocrH <= 0 || g.w <= 0 || g.h <= 0 {
		return fmt.Errorf("%w: hOCR %vx%v, PDF %vx%v", ErrPageGeometry, hocrW, hocrH, g.w, g.h)
	}
	want := g.w / g.h
	if math.Abs(hocrW/hocrH-want)/want > aspectTolerance {
		return fmt.Errorf("%w: hOCR %.0fx%.0f, PDF %.0fx%.0f pt", ErrPageGeometry, hocrW, hocrH, g.w, g.h)
	}
	return nil
}
