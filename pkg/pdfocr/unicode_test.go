package pdfocr

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/gardar/ocrchestra/pkg/hocr"
)

func TestDrawnTexts(t *testing.T) {
	cases := []struct {
		words, drawn []string
	}{
		{[]string{"Invoice", "date:", "28/09/2026"}, []string{"Invoice", "date:", "28/09/2026"}},
		{[]string{"الحسابات"}, []string{"تاباسحلا"}},
		{[]string{"رقم", "الفاتورة:", "٣٦٤"}, []string{"مقر", ":ةروتافلا", "٣٦٤"}},
		{[]string{"رقم٣"}, []string{"٣مقر"}},
		{[]string{"المبلغ", "12,500", "ر.س"}, []string{"غلبملا", "12,500", "س.ر"}},
		{[]string{"٢٠٢٦/٠٩/٢٨"}, []string{"٢٠٢٦/٠٩/٢٨"}},
		// A sign or terminator on a number in right-to-left text is displayed,
		// and so drawn, on the other side of the digits.
		{[]string{"الرصيد", "-5", "دولار"}, []string{"ديصرلا", "5-", "رالود"}},
		{[]string{"عربي12%"}, []string{"%12يبرع"}},
		{[]string{"السعر1,234.56$"}, []string{"$1,234.56رعسلا"}},
		// Not after Arabic letters, a number keeps its terminator.
		{[]string{"12%", "ربح"}, []string{"12%", "حبر"}},
		{[]string{"רווח", "12%"}, []string{"חוור", "12%"}},
		{[]string{"عربي", "abc-123"}, []string{"يبرع", "abc-123"}},
		{[]string{"بقيمة", "$5", "فقط"}, []string{"ةميقب", "5$", "طقف"}},
		// A Latin run keeps its order; where it goes depends on the line.
		{[]string{"ABCعربي"}, []string{"ABCيبرع"}},
		{[]string{"شركة", "ABCعربي"}, []string{"ةكرش", "يبرعABC"}},
		{[]string{"Invoice", "رقم", "5"}, []string{"Invoice", "مقر", "5"}},
		// Marks travel with their letters.
		{[]string{"مَرحَباً"}, []string{"ًابَحرَم"}},
		{[]string{"شركة", "cafe\u0301عربي"}, []string{"ةكرش", "يبرعcafe\u0301"}},
		{[]string{"שלום"}, []string{"םולש"}},
		// Brackets are not mirrored: extractors reverse them back.
		{[]string{"(عربي)"}, []string{")يبرع("}},
		{[]string{"[رقم", "5]", "(ABC)", "{x}"}, []string{"مقر[", "]5", ")ABC(", "}x{"}},
		// Inside a right-to-left word in a left-to-right line, neutrals and
		// joiners go with the letters around them.
		{[]string{"Total", "عربي-نص"}, []string{"Total", "صن-يبرع"}},
		{[]string{"abc", "عربي\u200cنص"}, []string{"abc", "صن\u200cيبرع"}},
	}
	for _, c := range cases {
		var words []hocr.Word
		for _, w := range c.words {
			words = append(words, hocr.Word{Text: w})
		}
		if got := drawnTexts(words); !slices.Equal(got, c.drawn) {
			t.Errorf("drawnTexts(%q) = %q, want %q", c.words, got, c.drawn)
		}
	}
}

// bidiCorpus holds lines covering the parts of the bidirectional algorithm
// bidiLevels implements.
var bidiCorpus = []string{
	"Invoice date: 28/09/2026",
	"الشركة العربية",
	"رقم الفاتورة: ٣٦٤",
	"المبلغ 12,500 ر.س",
	"الرصيد -5 دولار",
	"عربي12% بعد",
	"قبل عربي12% بعد",
	"السعر1,234.56$ فقط",
	"بقيمة $5 فقط",
	"Invoice رقم 5",
	"Total: 1,200.00 USD رقم ٣",
	"شركة ABC للتجارة ش.م.م.",
	"هاتف: 555/0100 - 555/0199",
	"تاريخ ٢٠٢٦/٠٩/٢٨ الساعة 10:30",
	"(عربي) abc",
	"abc (عربي) def",
	"عربي (abc) عربي",
	"[رقم 5] (ABC) {x}",
	"مَرحَباً بكم",
	"cafe\u0301 عربي",
	"شלום עולם 2026",
	"عربي\u200cعربي \u200dنص",
	"+1 555 0100 رقم",
	"50% خصم",
	"خصم 50%",
	"رقم 1.5.3 الفقرة",
	"٣٫١٤ و ١٬٠٠٠",
	"Total عربي-نص",
	"abc عربي، نص def",
	"abc عربي\u200cنص",
	"عربي abc \tdef",
	"عربي   ",
	"abc   ",
	"...",
	"",
}

// TestBidiLevelsMatchFribidi checks bidiLevels against GNU FriBidi, a full
// implementation of the algorithm, when its command-line tool is installed.
func TestBidiLevelsMatchFribidi(t *testing.T) {
	if _, err := exec.LookPath("fribidi"); err != nil {
		t.Skip("fribidi not installed")
	}
	cmd := exec.Command("fribidi", "--nopad", "--nobreak", "--nomirror", "--novisual", "--levels")
	cmd.Stdin = strings.NewReader(strings.Join(bidiCorpus, "\n") + "\n")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(want) != len(bidiCorpus) {
		t.Fatalf("fribidi returned %d lines for %d", len(want), len(bidiCorpus))
	}
	for i, line := range bidiCorpus {
		var got []string
		for _, l := range bidiLevels([]rune(line)) {
			got = append(got, strconv.Itoa(int(l)))
		}
		if g, w := strings.Join(got, " "), strings.TrimSpace(want[i]); g != w {
			t.Errorf("bidiLevels(%q)\n got %s\nwant %s", line, g, w)
		}
	}
}

func TestUnsupportedRunes(t *testing.T) {
	if got := UnsupportedRunes("Café 12", DefaultFont); len(got) != 0 {
		t.Errorf("core font should draw Latin-1, missing %q", string(got))
	}
	if got := UnsupportedRunes("الحسابات", DefaultFont); len(got) == 0 {
		t.Error("core font must report Arabic as unsupported")
	}
	if got := UnsupportedRunes("الحسابات العامة ٣ Café Ωμέγα Привет שלום", UnicodeFont); len(got) != 0 {
		t.Errorf("Unicode font should cover Arabic, Latin, Greek, Cyrillic, Hebrew; missing %q", string(got))
	}
	// DejaVu Sans has no glyphs for these, but they are still encoded and
	// extract correctly.
	if got := UnsupportedRunes("東京 ភាសា ภาษา हिन्दी", UnicodeFont); len(got) != 0 {
		t.Errorf("characters without a glyph are still extractable; reported %q", string(got))
	}
	if got := UnsupportedRunes("ok 😀 𝑥", UnicodeFont); string(got) != "😀𝑥" {
		t.Errorf("characters beyond the Basic Multilingual Plane cannot be encoded; got %q", string(got))
	}
}

// TestUnicodeLayerMissingAndUnencodableCharacters draws a character the font
// has no glyph for, which must still extract, and characters beyond the Basic
// Multilingual Plane, which fpdf cannot encode and the layer leaves out.
func TestUnicodeLayerMissingAndUnencodableCharacters(t *testing.T) {
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("pdftotext not installed")
	}
	box := func(x1, y1, x2, y2 float64) hocr.BoundingBox { return hocr.BoundingBox{X1: x1, Y1: y1, X2: x2, Y2: y2} }
	line := func(y float64, words ...string) hocr.Line {
		l := hocr.Line{BBox: box(100, y, 1500, y+50)}
		x := 150.0
		for _, w := range words {
			width := 40.0 * float64(len([]rune(w)))
			l.Words = append(l.Words, hocr.Word{Text: w, BBox: box(x, y, x+width, y+50)})
			x += width + 40
		}
		return l
	}
	page := hocr.Page{ID: "page_1", PageNumber: 1, BBox: box(0, 0, 1654, 2339), Lines: []hocr.Line{
		line(150, "Invoice", "東京", "total"),
		line(300, "smile", "😀", "now"),
		line(450, "sum𝑥", "end"),
	}}
	// Enough plain words that the few left out stay under the layer's
	// limit on text it cannot encode.
	for i := 0; i < 12; i++ {
		page.Lines = append(page.Lines, line(600+float64(i)*100, "plain", "words", "here"))
	}

	got := extractedLines(extractLayer(t, page))
	for _, want := range []string{"Invoice 東京 total", "smile now", "sum end"} {
		if !slices.Contains(got, want) {
			t.Errorf("no extracted line %q in %q", want, got)
		}
	}
}

// TestUnicodeLayerRejectsMostlyUnencodableText keeps the layer's limit: a page
// on which more than a tenth of the words cannot be encoded is an error.
func TestUnicodeLayerRejectsMostlyUnencodableText(t *testing.T) {
	box := func(x1, y1, x2, y2 float64) hocr.BoundingBox { return hocr.BoundingBox{X1: x1, Y1: y1, X2: x2, Y2: y2} }
	page := hocr.Page{ID: "page_1", PageNumber: 1, BBox: box(0, 0, 1000, 1000), Lines: []hocr.Line{{
		BBox: box(100, 100, 900, 150),
		Words: []hocr.Word{
			{Text: "😀", BBox: box(100, 100, 150, 150)},
			{Text: "text", BBox: box(200, 100, 350, 150)},
			{Text: "𝑥", BBox: box(400, 100, 450, 150)},
		},
	}}}
	img := image.NewGray(image.Rect(0, 0, 1000, 1000))
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, img); err != nil {
		t.Fatal(err)
	}
	config := DefaultConfig()
	config.Font, config.LogWarnings = UnicodeFont, false
	if _, err := AssembleWithOCR(&hocr.HOCR{Pages: []hocr.Page{page}}, [][]byte{pngData.Bytes()}, config); err == nil ||
		!strings.Contains(err.Error(), "character encoding issues in 2 of 3 words") {
		t.Errorf("AssembleWithOCR error = %v, want the encoding limit", err)
	}
}

// TestUnicodeLayerRoundTrip draws words the way an OCR page places them and
// extracts the PDF with pdftotext -layout, the extractor paperless-ngx uses.
//
// pdftotext rebuilds reading order from the drawn (visual) order with a
// simplified bidi pass that classes only letters and numbers: in a
// right-to-left line, a colon, sign or space next to a number can land on the
// wrong side of it ("رقم الفاتورة: ٣٦٤" comes back as "رقم الفاتورة٣٦٤ :"). Any correctly
// drawn PDF extracts that way, so such lines are checked for their letters and
// digits in reading order rather than character for character.
func TestUnicodeLayerRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("pdftotext not installed")
	}

	const width, height = 1654.0, 2339.0
	// Each line lists words in reading order; right-to-left lines are placed
	// from the right edge leftwards, as they appear on a scan.
	lines := []struct {
		words []string
		rtl   bool
		exact bool
	}{
		{[]string{"الشركة", "العربية"}, true, true},
		{[]string{"قسم", "الحسابات", "العامة"}, true, true},
		{[]string{"رقم", "الفاتورة:", "٣٦٤"}, true, false},
		{[]string{"المبلغ", "12,500", "ر.س"}, true, false},
		{[]string{"ACME", "TRADING", "LTD"}, false, true},
		{[]string{"Invoice", "date:", "28/09/2026"}, false, true},
	}

	page := hocr.Page{ID: "page_1", PageNumber: 1, BBox: hocr.BoundingBox{X2: width, Y2: height}}
	for li, line := range lines {
		y := 150.0 + float64(li)*120
		hl := hocr.Line{BBox: hocr.BoundingBox{X1: 100, Y1: y, X2: width - 100, Y2: y + 50}}
		x := 150.0
		if line.rtl {
			x = width - 150
		}
		for _, w := range line.words {
			ww := 30.0 * float64(len([]rune(w)))
			bb := hocr.BoundingBox{X1: x, Y1: y, X2: x + ww, Y2: y + 50}
			if line.rtl {
				bb = hocr.BoundingBox{X1: x - ww, Y1: y, X2: x, Y2: y + 50}
				x -= ww + 40
			} else {
				x += ww + 40
			}
			hl.Words = append(hl.Words, hocr.Word{Text: w, BBox: bb})
		}
		page.Lines = append(page.Lines, hl)
	}

	out := extractLayer(t, page)
	got := extractedLines(out)
	if len(got) != len(lines) {
		t.Fatalf("extracted %d lines, want %d:\n%s", len(got), len(lines), out)
	}
	for i, line := range lines {
		want := strings.Join(line.words, " ")
		if line.exact && got[i] != want {
			t.Errorf("line %d = %q, want %q", i+1, got[i], want)
		}
		if g, w := letterDigitRuns(got[i]), letterDigitRuns(want); !slices.Equal(g, w) {
			t.Errorf("line %d = %q: letters and digits %q, want %q", i+1, got[i], g, w)
		}
	}

	// Words are drawn left to right whatever their direction, as a word
	// processor lays out a line; extractors that follow drawing order (such as
	// PDFKit) otherwise split a right-to-left line into one line per word. In
	// drawing order, a right-to-left line's words therefore come last to first.
	// (pdftotext -raw keeps the drawing order of words; the order of characters
	// inside a word is not compared.)
	raw := extractedLines(extractLayer(t, page, "-raw"))
	if len(raw) != len(lines) {
		t.Fatalf("raw extraction gave %d lines, want %d: %q", len(raw), len(lines), raw)
	}
	for i, line := range lines {
		drawn := slices.Clone(line.words)
		if line.rtl {
			slices.Reverse(drawn)
		}
		got := strings.Fields(raw[i])
		same := len(got) == len(drawn)
		for j := 0; same && j < len(got); j++ {
			same = slices.Equal(sortedRunes(got[j]), sortedRunes(drawn[j]))
		}
		if !same {
			t.Errorf("line %d drawn as %q, want words in the order %q", i+1, raw[i], drawn)
		}
	}
}

func sortedRunes(s string) []rune {
	r := []rune(s)
	slices.Sort(r)
	return r
}

// letterDigitRuns splits s into its runs of letters and runs of digits, in order.
func letterDigitRuns(s string) []string {
	var runs []string
	var run []rune
	kind := 0 // 1 letters, 2 digits
	for _, r := range s {
		k := 0
		switch {
		case unicode.IsLetter(r) || unicode.IsMark(r):
			k = 1
		case unicode.IsDigit(r):
			k = 2
		}
		if k != kind && len(run) > 0 {
			runs = append(runs, string(run))
			run = run[:0]
		}
		if kind = k; k != 0 {
			run = append(run, r)
		}
	}
	if len(run) > 0 {
		runs = append(runs, string(run))
	}
	return runs
}

// extractLayer draws page as a Unicode text layer over a blank page image and
// returns what pdftotext extracts from it: with -layout, or in the order the
// text is drawn with -raw.
func extractLayer(t *testing.T, page hocr.Page, mode ...string) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, int(page.BBox.X2), int(page.BBox.Y2)))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	img.Set(0, 0, color.Black)
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, img); err != nil {
		t.Fatal(err)
	}

	config := DefaultConfig()
	config.Font = UnicodeFont
	config.LogWarnings = false
	pdfData, err := AssembleWithOCR(&hocr.HOCR{Pages: []hocr.Page{page}}, [][]byte{pngData.Bytes()}, config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "layer.pdf")
	if dump := os.Getenv("PDFOCR_DUMP"); dump != "" {
		path = dump
	}
	if err := os.WriteFile(path, pdfData, 0o644); err != nil {
		t.Fatal(err)
	}
	if len(mode) == 0 {
		mode = []string{"-layout"}
	}
	out, err := exec.Command("pdftotext", append(append([]string{"-q", "-enc", "UTF-8"}, mode...), path, "-")...).Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// TestUnicodeLayerRotatedLines draws lines turned by their hOCR textangle.
func TestUnicodeLayerRotatedLines(t *testing.T) {
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("pdftotext not installed")
	}
	box := func(x1, y1, x2, y2 float64) hocr.BoundingBox { return hocr.BoundingBox{X1: x1, Y1: y1, X2: x2, Y2: y2} }
	angle := func(degrees string) map[string]string { return map[string]string{"textangle": degrees} }
	page := hocr.Page{ID: "page_1", PageNumber: 1, BBox: box(0, 0, 1654, 2339), Lines: []hocr.Line{
		{BBox: box(150, 150, 550, 200), Words: []hocr.Word{
			{Text: "Invoice", BBox: box(150, 150, 360, 200)},
			{Text: "total", BBox: box(400, 150, 550, 200)},
		}},
		// Turned counterclockwise: reads bottom to top.
		{BBox: box(1500, 700, 1550, 1100), Metadata: angle("90"), Words: []hocr.Word{
			{Text: "STAMP", BBox: box(1500, 900, 1550, 1100)},
			{Text: "2025", BBox: box(1500, 700, 1550, 860)},
		}},
		// Turned clockwise: left-to-right text reads top to bottom, right-to-left
		// text bottom to top.
		{BBox: box(100, 600, 150, 1000), Metadata: angle("-90"), Words: []hocr.Word{
			{Text: "طابع", BBox: box(100, 800, 150, 1000)},
			{Text: "مالي", BBox: box(100, 600, 150, 760)},
		}},
		// Upside down.
		{BBox: box(700, 2000, 1000, 2050), Metadata: angle("180"), Words: []hocr.Word{
			{Text: "Page", BBox: box(870, 2000, 1000, 2050)},
			{Text: "two", BBox: box(700, 2000, 830, 2050)},
		}},
	}}

	got := extractedLines(extractLayer(t, page))
	for _, want := range []string{"Invoice total", "STAMP 2025", "طابع مالي", "Page two"} {
		if !slices.Contains(got, want) {
			t.Errorf("no extracted line %q in %q", want, got)
		}
	}
}

// TestUnicodeLayerTouchingWords checks that words whose OCR boxes touch or
// overlap, as neighbouring words on a scan often do, still extract as words.
func TestUnicodeLayerTouchingWords(t *testing.T) {
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("pdftotext not installed")
	}
	box := func(x1, y1, x2, y2 float64) hocr.BoundingBox { return hocr.BoundingBox{X1: x1, Y1: y1, X2: x2, Y2: y2} }
	page := hocr.Page{ID: "page_1", PageNumber: 1, BBox: box(0, 0, 1654, 2339), Lines: []hocr.Line{
		{BBox: box(150, 150, 700, 200), Words: []hocr.Word{
			{Text: "touching", BBox: box(150, 150, 400, 200)},
			{Text: "boxes", BBox: box(400, 150, 560, 200)},
			{Text: "overlap", BBox: box(550, 150, 700, 200)},
		}},
		{BBox: box(900, 300, 1500, 350), Words: []hocr.Word{
			{Text: "كتاب", BBox: box(1380, 300, 1500, 350)},
			{Text: "قلم", BBox: box(1250, 300, 1392, 350)},
			{Text: "هاتف", BBox: box(1100, 300, 1250, 350)},
		}},
		// Punctuation that the OCR reports as its own word, attached.
		{BBox: box(150, 450, 500, 500), Words: []hocr.Word{
			{Text: "Fax", BBox: box(150, 450, 240, 500)},
			{Text: "(", BBox: box(270, 450, 285, 500), Metadata: map[string]string{hocr.NoSpaceAfter: "1"}},
			{Text: "555", BBox: box(287, 450, 380, 500), Metadata: map[string]string{hocr.NoSpaceAfter: "1"}},
			{Text: ")", BBox: box(382, 450, 397, 500)},
		}},
	}}
	got := extractedLines(extractLayer(t, page))
	for _, want := range []string{"touching boxes overlap", "كتاب قلم هاتف", "Fax (555)"} {
		if !slices.Contains(got, want) {
			t.Errorf("no extracted line %q in %q", want, got)
		}
	}
}

func TestJoinWords(t *testing.T) {
	noSpace := map[string]string{hocr.NoSpaceAfter: "1"}
	words := []hocr.Word{
		{Text: "Fax", BBox: hocr.BoundingBox{X1: 0, Y1: 10, X2: 30, Y2: 20}},
		{Text: "(", BBox: hocr.BoundingBox{X1: 40, Y1: 9, X2: 44, Y2: 21}, Metadata: noSpace},
		{Text: "555", BBox: hocr.BoundingBox{X1: 44, Y1: 10, X2: 70, Y2: 20}, Metadata: noSpace},
		{Text: ")", BBox: hocr.BoundingBox{X1: 70, Y1: 9, X2: 74, Y2: 21}},
		{Text: "end", BBox: hocr.BoundingBox{X1: 80, Y1: 10, X2: 100, Y2: 20}, Metadata: noSpace},
	}
	got := joinWords(words)
	want := []hocr.Word{
		{Text: "Fax", BBox: words[0].BBox},
		{Text: "(555)", BBox: hocr.BoundingBox{X1: 40, Y1: 9, X2: 74, Y2: 21}, Metadata: noSpace},
		{Text: "end", BBox: words[4].BBox, Metadata: noSpace},
	}
	if len(got) != len(want) {
		t.Fatalf("joinWords gave %d words, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Text != want[i].Text || got[i].BBox != want[i].BBox {
			t.Errorf("word %d = %q %+v, want %q %+v", i, got[i].Text, got[i].BBox, want[i].Text, want[i].BBox)
		}
	}
	if words[1].Text != "(" || words[1].BBox.X2 != 44 {
		t.Error("joinWords modified its input")
	}
}

func TestTextAngle(t *testing.T) {
	for in, want := range map[string]float64{"": 0, "0": 0, "90": 90, " 180 ": 180, "270": 270, "-90": 270, "450": 90, "45": 0, "x": 0} {
		if got := textAngle(hocr.Line{Metadata: map[string]string{"textangle": in}}); got != want {
			t.Errorf("textAngle(%q) = %v, want %v", in, got, want)
		}
	}
}

// extractedLines normalizes pdftotext output: drops bidi control characters,
// collapses runs of spaces and removes empty lines.
func extractedLines(s string) []string {
	strip := strings.NewReplacer(
		"\u200e", "", "\u200f", "", "\u202a", "", "\u202b", "", "\u202c", "", "\u202d", "", "\u202e", "",
		"\u2066", "", "\u2067", "", "\u2068", "", "\u2069", "", "\f", "",
	)
	var lines []string
	for _, l := range strings.Split(strip.Replace(s), "\n") {
		if l = strings.Join(strings.Fields(l), " "); l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}
