package pdfocr

import (
	"bytes"
	"fmt"
	"io"

	"codeberg.org/go-pdf/fpdf"
	"codeberg.org/go-pdf/fpdf/contrib/gofpdi"

	"github.com/gardar/ocrchestra/pkg/hocr"
)

// modifyExistingPDF imports pages from an existing PDF and overlays OCR text layer.
func modifyExistingPDF(
	inputPDFData []byte,
	hOCRData hocr.HOCR,
	startFromPage int,
	debug bool,
	layerName string,
	fontConfig FontConfig,
) ([]byte, error) {

	rs := io.ReadSeeker(bytes.NewReader(inputPDFData))
	pages, err := pageGeometries(rs)
	if err != nil {
		return nil, err
	}

	pdf := fpdf.New("P", "pt", "", "")
	importer := gofpdi.NewImporter()

	for i, page := range hOCRData.Pages {
		targetPage := i + startFromPage

		// Calculate the actual page number in the PDF
		actualPageNum := i + 1 // 1-based page number in the resulting PDF

		geom, ok := pages[targetPage]
		if !ok {
			return nil, fmt.Errorf("hOCR page %d has no page %d in the PDF", i+1, targetPage)
		}
		hocrW, hocrH := page.BBox.X2, page.BBox.Y2
		if err := geom.checkAspect(hocrW, hocrH); err != nil {
			return nil, fmt.Errorf("page %d: %w", targetPage, err)
		}

		// The output page keeps the original page's size; the hOCR, measured in
		// pixels of the image the OCR engine saw, is scaled onto it.
		pdf.AddPageFormat("P", fpdf.SizeType{Wd: geom.w, Ht: geom.h})

		tpl := importer.ImportPageFromStream(pdf, &rs, targetPage, "/MediaBox")
		importer.UseImportedTemplate(pdf, tpl, 0, 0, geom.w, geom.h)

		transform := func(x, y float64) (float64, float64) {
			return normalizeCoords(x, y, hocrW, hocrH, geom.w, geom.h)
		}

		// Pass the page number to drawOCRLayer
		drawOCRLayer(pdf, page, debug, layerName, actualPageNum, transform, fontConfig)
	}

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
