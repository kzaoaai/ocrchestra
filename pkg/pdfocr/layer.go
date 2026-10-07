package pdfocr

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"

	"codeberg.org/go-pdf/fpdf"
	"golang.org/x/text/encoding/charmap"

	"github.com/gardar/ocrchestra/pkg/hocr"
)

// drawOCRLayer draws the OCR text onto a layer in a pdf page.
// The pageNum parameter is used to create unique layer names for each page.
func drawOCRLayer(
	pdf *fpdf.Fpdf,
	page hocr.Page,
	debug bool,
	layerName string,
	pageNum int,
	transform func(x, y float64) (float64, float64),
	fontConfig FontConfig,
) error {
	// Format layer name with page number if not already included
	formattedLayerName := layerName
	if pageNum > 0 {
		formattedLayerName = fmt.Sprintf("%s (Page %d)", layerName, pageNum)
	}

	layer := pdf.AddLayer(formattedLayerName, true)
	pdf.BeginLayer(layer)
	if len(fontConfig.UTF8) > 0 {
		// Registering the same font again is a no-op, so every page may call this.
		pdf.AddUTF8FontFromBytes(fontConfig.Name, fontConfig.Style, fontConfig.UTF8)
	}
	pdf.SetFont(fontConfig.Name, fontConfig.Style, fontConfig.Size)

	if debug {
		pdf.SetTextColor(255, 0, 0) // highlight text in red
	} else {
		pdf.SetAlpha(0.0, "Normal") // hide text from normal view
	}

	encodingErrors := 0
	wordCount := 0

	if len(fontConfig.UTF8) > 0 {
		wordCount = drawUnicodeWords(pdf, page, transform, fontConfig, debug, &encodingErrors)
	} else {
		// Process words from areas
		for _, area := range page.Areas {
			// Words directly under area
			for _, word := range area.Words {
				drawWord(pdf, word, transform, fontConfig, debug, &encodingErrors)
				wordCount++
			}

			// Words in lines under area
			for _, line := range area.Lines {
				for _, word := range line.Words {
					drawWord(pdf, word, transform, fontConfig, debug, &encodingErrors)
					wordCount++
				}
			}

			// Process words from paragraphs under area
			for _, paragraph := range area.Paragraphs {
				// Words directly under paragraph
				for _, word := range paragraph.Words {
					drawWord(pdf, word, transform, fontConfig, debug, &encodingErrors)
					wordCount++
				}

				// Words in lines under paragraph
				for _, line := range paragraph.Lines {
					for _, word := range line.Words {
						drawWord(pdf, word, transform, fontConfig, debug, &encodingErrors)
						wordCount++
					}
				}
			}
		}

		// Process words from paragraphs directly under page
		for _, paragraph := range page.Paragraphs {
			// Words directly under paragraph
			for _, word := range paragraph.Words {
				drawWord(pdf, word, transform, fontConfig, debug, &encodingErrors)
				wordCount++
			}

			// Words in lines under paragraph
			for _, line := range paragraph.Lines {
				for _, word := range line.Words {
					drawWord(pdf, word, transform, fontConfig, debug, &encodingErrors)
					wordCount++
				}
			}
		}

		// Process words from lines directly under page
		for _, line := range page.Lines {
			for _, word := range line.Words {
				drawWord(pdf, word, transform, fontConfig, debug, &encodingErrors)
				wordCount++
			}
		}

	}

	pdf.EndLayer()

	// Report encoding errors if more than a threshold
	if wordCount > 0 && encodingErrors > 0 && encodingErrors > wordCount/10 {
		return fmt.Errorf("character encoding issues in %d of %d words",
			encodingErrors, wordCount)
	}

	return nil
}

// drawWord renders a single word onto the PDF layer
func drawWord(pdf *fpdf.Fpdf, word hocr.Word, transform func(x, y float64) (float64, float64),
	fontConfig FontConfig, debug bool, encodingErrors *int) {

	x, y := transform(word.BBox.X1, word.BBox.Y1)
	x2, _ := transform(word.BBox.X2, word.BBox.Y1)
	wordWidth := x2 - x

	// Convert text to ISO-8859-1 to avoid PDF encoding issues
	latin1, err := charmap.ISO8859_1.NewEncoder().String(word.Text)
	if err != nil {
		// Track encoding errors but continue
		*encodingErrors++
		latin1 = word.Text // fallback to raw text
	}

	strWidth := pdf.GetStringWidth(latin1)
	if strWidth > 0 {
		scale := wordWidth / strWidth
		pdf.SetFontSize(fontConfig.Size * scale)
	}

	fontSize, _ := pdf.GetFontSize()
	y += fontSize * fontConfig.AscentRatio

	pdf.Text(x, y, latin1)
	pdf.SetFontSize(fontConfig.Size)

	if debug {
		_, y1 := transform(word.BBox.X1, word.BBox.Y1)
		_, y2 := transform(word.BBox.X1, word.BBox.Y2)
		height := y2 - y1
		pdf.Rect(x, y-(fontSize*fontConfig.AscentRatio), wordWidth, height, "D")
	}
}

// drawUnicodeWords draws a page's words with an embedded Unicode font and
// returns how many it drew. It visits the same words as the core-font path.
func drawUnicodeWords(pdf *fpdf.Fpdf, page hocr.Page, transform func(x, y float64) (float64, float64),
	fontConfig FontConfig, debug bool, encodingErrors *int) int {

	count := 0
	lines := func(lines []hocr.Line) {
		for _, line := range lines {
			drawUnicodeLine(pdf, line.Words, line.BBox, textAngle(line), transform, fontConfig, debug, encodingErrors)
			count += len(line.Words)
		}
	}
	// A word outside any line is drawn as a line of its own.
	words := func(words []hocr.Word) {
		for _, word := range words {
			drawUnicodeLine(pdf, []hocr.Word{word}, word.BBox, 0, transform, fontConfig, debug, encodingErrors)
			count++
		}
	}

	for _, area := range page.Areas {
		words(area.Words)
		lines(area.Lines)
		for _, paragraph := range area.Paragraphs {
			words(paragraph.Words)
			lines(paragraph.Lines)
		}
	}
	for _, paragraph := range page.Paragraphs {
		words(paragraph.Words)
		lines(paragraph.Lines)
	}
	lines(page.Lines)
	return count
}

const (
	// lineDescentShare is the part of a word's height below the baseline.
	lineDescentShare = 0.2
	// wordGap is the least gap left between neighbouring words, as a fraction
	// of the font size. pdftotext sees a word break between upright words from
	// about 0.04; 0.15 leaves a margin. (On rotated lines it does not break an
	// Arabic-Indic number from a following Arabic word short of a gap of about
	// a font size, which no margin covers; PDFKit does.)
	wordGap = 0.15
)

// drawUnicodeLine draws the words of one line at a single font size on a
// common baseline. Text extractors such as pdftotext group glyphs into lines
// by font size and baseline, so words drawn at sizes fitted to their own
// widths scatter across several extracted lines. Each word is instead
// stretched to the width of its box with horizontal scaling (Tz), which
// extractors do not treat as a change of size.
//
// angle is the line's hOCR textangle: text turned counterclockwise by 90, 180
// or 270 degrees is laid out unturned and drawn rotated into place.
func drawUnicodeLine(pdf *fpdf.Fpdf, words []hocr.Word, lineBox hocr.BoundingBox, angle float64,
	transform func(x, y float64) (float64, float64), fontConfig FontConfig, debug bool, encodingErrors *int) {

	for _, word := range words {
		if len(UnsupportedRunes(word.Text, fontConfig)) > 0 {
			*encodingErrors++
		}
	}
	words = encodableWords(joinWords(words))
	if len(words) == 0 {
		return
	}
	texts := drawnTexts(words)

	// Word boxes in page coordinates, turned back by the text angle about the
	// centre of the line so that the text runs left to right.
	lx1, ly1 := transform(lineBox.X1, lineBox.Y1)
	lx2, ly2 := transform(lineBox.X2, lineBox.Y2)
	cx, cy := (lx1+lx2)/2, (ly1+ly2)/2
	boxes := make([]rect, len(words))
	heights := make([]float64, len(words))
	bottoms := make([]float64, len(words))
	for i, word := range words {
		x1, y1 := transform(word.BBox.X1, word.BBox.Y1)
		x2, y2 := transform(word.BBox.X2, word.BBox.Y2)
		boxes[i] = rect{min(x1, x2), min(y1, y2), max(x1, x2), max(y1, y2)}.rotate(-angle, cx, cy)
		heights[i] = boxes[i].y2 - boxes[i].y1
		bottoms[i] = boxes[i].y2
	}
	// The line's own box can span words set at different heights, so the font
	// size and baseline follow the typical word instead.
	height := median(heights)
	if height <= 0 {
		return
	}
	baseline := median(bottoms) - lineDescentShare*height

	// Extractors find word boundaries from the gap between words, and the boxes
	// of neighbouring words on a scan often touch or overlap. Pull such
	// neighbours apart so that a gap of wordGap font sizes separates them.
	gap := wordGap * height
	order := make([]int, len(words))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return boxes[order[a]].x1+boxes[order[a]].x2 < boxes[order[b]].x1+boxes[order[b]].x2
	})
	for k := 1; k < len(order); k++ {
		left, right := &boxes[order[k-1]], &boxes[order[k]]
		if right.x1-left.x2 >= gap {
			continue
		}
		mid := (left.x2 + right.x1) / 2
		// Never shrink a word below half its width.
		left.x2 = math.Max(mid-gap/2, (left.x1+left.x2)/2)
		right.x1 = math.Min(mid+gap/2, (right.x1+right.x2)/2)
	}

	if angle != 0 {
		pdf.TransformBegin()
		pdf.TransformRotate(angle, cx, cy)
	}
	pdf.SetFontSize(height * pdf.GetConversionRatio())

	// Words are drawn left to right, the order in which text of either direction
	// is laid out on the page; extractors read a leftward jump between runs on
	// one baseline as the start of a new line.
	spaceWidth := pdf.GetStringWidth(" ")
	for k, i := range order {
		box := boxes[i]
		text := texts[i]
		textWidth := pdf.GetStringWidth(text)
		if textWidth <= 0 || box.x2 <= box.x1 {
			continue
		}
		pdf.RawWriteStr(fmt.Sprintf("%.2f Tz", 100*(box.x2-box.x1)/textWidth))
		pdf.Text(box.x1, baseline, text)

		if debug {
			pdf.Rect(box.x1, box.y1, box.x2-box.x1, box.y2-box.y1, "D")
		}

		// An explicit space in the gap to the next word keeps the boundary for
		// extractors that read space characters rather than gaps.
		if k+1 < len(order) && spaceWidth > 0 {
			next := boxes[order[k+1]]
			scale := math.Max(1, 100*(next.x1-box.x2)/spaceWidth)
			pdf.RawWriteStr(fmt.Sprintf("%.2f Tz", scale))
			pdf.Text(box.x2, baseline, " ")
		}
	}
	pdf.RawWriteStr("100 Tz")
	if angle != 0 {
		pdf.TransformEnd()
	}
	pdf.SetFontSize(fontConfig.Size)
}

// drawnTexts returns the text of each word of a line in the order its
// characters are drawn: the visual order the bidirectional algorithm gives
// them in the line, read in hOCR (logical) order with single spaces.
func drawnTexts(words []hocr.Word) []string {
	var line []rune
	spans := make([][2]int, len(words))
	for i, word := range words {
		if i > 0 {
			line = append(line, ' ')
		}
		spans[i][0] = len(line)
		line = append(line, []rune(word.Text)...)
		spans[i][1] = len(line)
	}
	levels := bidiLevels(line)
	texts := make([]string, len(words))
	for i, span := range spans {
		texts[i] = string(visualOrder(line[span[0]:span[1]], levels[span[0]:span[1]]))
	}
	return texts
}

// textAngle returns a line's hOCR textangle when it is a quarter turn (90, 180
// or 270 degrees counterclockwise) and 0 otherwise.
func textAngle(line hocr.Line) float64 {
	angle, err := strconv.ParseFloat(strings.TrimSpace(line.Metadata["textangle"]), 64)
	if err != nil {
		return 0
	}
	angle = math.Mod(math.Mod(angle, 360)+360, 360)
	switch angle {
	case 90, 180, 270:
		return angle
	}
	return 0
}

// rect is an axis-aligned box in page coordinates, y growing downwards.
type rect struct{ x1, y1, x2, y2 float64 }

// rotate turns r counterclockwise, as seen on the page, by angle degrees about
// (cx, cy) and returns the box that encloses the result.
func (r rect) rotate(angle, cx, cy float64) rect {
	if angle == 0 {
		return r
	}
	sin, cos := math.Sincos(angle * math.Pi / 180)
	out := rect{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	for _, p := range [4][2]float64{{r.x1, r.y1}, {r.x2, r.y1}, {r.x1, r.y2}, {r.x2, r.y2}} {
		dx, dy := p[0]-cx, p[1]-cy
		x, y := cx+dx*cos+dy*sin, cy-dx*sin+dy*cos
		out.x1, out.y1 = math.Min(out.x1, x), math.Min(out.y1, y)
		out.x2, out.y2 = math.Max(out.x2, x), math.Max(out.y2, y)
	}
	return out
}

func median(values []float64) float64 {
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

// joinWords merges each word marked hocr.NoSpaceAfter with the word after it,
// so that punctuation stays attached to its word instead of being drawn, and
// extracted, as a word of its own.
func joinWords(words []hocr.Word) []hocr.Word {
	joined := make([]hocr.Word, 0, len(words))
	attach := false
	for _, word := range words {
		if attach {
			last := &joined[len(joined)-1]
			last.Text += word.Text
			last.BBox = hocr.BoundingBox{
				X1: math.Min(last.BBox.X1, word.BBox.X1),
				Y1: math.Min(last.BBox.Y1, word.BBox.Y1),
				X2: math.Max(last.BBox.X2, word.BBox.X2),
				Y2: math.Max(last.BBox.Y2, word.BBox.Y2),
			}
		} else {
			joined = append(joined, word)
		}
		attach = word.Metadata[hocr.NoSpaceAfter] != ""
	}
	return joined
}
