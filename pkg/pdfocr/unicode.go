package pdfocr

import (
	_ "embed"
	"slices"
	"sort"
	"sync"
	"unicode"

	"golang.org/x/image/font/sfnt"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/unicode/bidi"
)

// dejaVuSans is DejaVu Sans 2.37 (see fonts/LICENSE-DejaVu). It covers Latin,
// Greek, Cyrillic, Arabic and Hebrew; the layer is invisible, so what matters
// is that its characters map back to Unicode when text is extracted.
//
//go:embed fonts/DejaVuSans.ttf
var dejaVuSans []byte

// UnicodeFont draws the OCR layer with an embedded Unicode font instead of a
// PDF core font, so text outside ISO-8859-1 (Arabic, Hebrew, Greek, Cyrillic,
// ...) is extractable, and right-to-left text is drawn in visual order.
var UnicodeFont = FontConfig{
	Name:        "DejaVuSans",
	Style:       "",
	Size:        10,
	AscentRatio: 0.729, // cap height / em of DejaVu Sans
	UTF8:        dejaVuSans,
}

// UnsupportedRunes returns the characters of text that font cannot draw as
// extractable text: for a core font anything outside ISO-8859-1, for a UTF-8
// font anything missing from its character map. Spaces and control characters
// are ignored.
func UnsupportedRunes(text string, font FontConfig) []rune {
	var missing []rune
	if len(font.UTF8) == 0 {
		encoder := charmap.ISO8859_1.NewEncoder()
		for _, r := range text {
			if unicode.IsSpace(r) || unicode.IsControl(r) {
				continue
			}
			if _, err := encoder.String(string(r)); err != nil {
				missing = append(missing, r)
			}
		}
		return missing
	}

	parsed, err := parsedFont(font.UTF8)
	if err != nil {
		// An unreadable font draws nothing faithfully.
		for _, r := range text {
			if !unicode.IsSpace(r) && !unicode.IsControl(r) {
				missing = append(missing, r)
			}
		}
		return missing
	}
	var buf sfnt.Buffer
	for _, r := range text {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			continue
		}
		if idx, err := parsed.GlyphIndex(&buf, r); err != nil || idx == 0 {
			missing = append(missing, r)
		}
	}
	return missing
}

var parsedFonts sync.Map // *byte (first byte of the font data) -> *sfnt.Font

func parsedFont(data []byte) (*sfnt.Font, error) {
	key := &data[0]
	if f, ok := parsedFonts.Load(key); ok {
		return f.(*sfnt.Font), nil
	}
	f, err := sfnt.Parse(data)
	if err != nil {
		return nil, err
	}
	parsedFonts.Store(key, f)
	return f, nil
}

// Bidirectional text. PDF text is laid out left to right, and extractors
// rebuild reading order from the drawn (visual) order: PDFKit and other viewers
// with the Unicode Bidirectional Algorithm, pdftotext with a simpler
// approximation of it. Right-to-left text is therefore drawn in the visual
// order the algorithm gives. Word placement comes from the OCR boxes; what is
// resolved here is the order of the characters inside each word, which depends
// on the rest of its line: "-5" in an Arabic line is drawn "5-", as it is
// displayed. (pdftotext's approximation puts such a sign, and a space next to
// a number, on the wrong side of the digits.)

// bidiLevels resolves the embedding level of each rune of a line of text with
// the Unicode Bidirectional Algorithm (UAX #9) for text without explicit
// directional formatting, which is ignored: the paragraph direction comes from
// the first strong character (rules P2, P3); then weak types (W1-W7), paired
// brackets (N0), other neutrals (N1, N2) and implicit levels (I1, I2) are
// resolved, and trailing whitespace is reset (L1).
func bidiLevels(runes []rune) []uint8 {
	n := len(runes)
	levels := make([]uint8, n)
	original := make([]bidi.Class, n)
	for i, r := range runes {
		props, _ := bidi.LookupRune(r)
		original[i] = props.Class()
	}

	// X9: explicit formatting characters and boundary neutrals take no part;
	// they get the level of the character before them.
	var idx []int
	for i, c := range original {
		if !removedByX9(c) {
			idx = append(idx, i)
		}
	}
	types := make([]bidi.Class, len(idx))
	for k, i := range idx {
		types[k] = original[i]
	}

	// P2, P3.
	var paragraph uint8
	for _, c := range types {
		if c == bidi.L {
			break
		}
		if c == bidi.R || c == bidi.AL {
			paragraph = 1
			break
		}
	}
	sos := bidi.L // with a single level run, sos and eos are the paragraph direction
	if paragraph == 1 {
		sos = bidi.R
	}

	// W1: a non-spacing mark takes the type of the character before it.
	for k, c := range types {
		if c == bidi.NSM {
			if k == 0 {
				types[k] = sos
			} else {
				types[k] = types[k-1]
			}
		}
	}
	// W2: a European number after Arabic letters is an Arabic number.
	// W3: Arabic letters are right-to-left.
	lastStrong := sos
	for k, c := range types {
		switch c {
		case bidi.L, bidi.R:
			lastStrong = c
		case bidi.AL:
			lastStrong = c
			types[k] = bidi.R
		case bidi.EN:
			if lastStrong == bidi.AL {
				types[k] = bidi.AN
			}
		}
	}
	// W4: a single separator between two numbers of the same kind joins them.
	for k := 1; k+1 < len(types); k++ {
		before, after := types[k-1], types[k+1]
		switch types[k] {
		case bidi.ES:
			if before == bidi.EN && after == bidi.EN {
				types[k] = bidi.EN
			}
		case bidi.CS:
			if before == after && (before == bidi.EN || before == bidi.AN) {
				types[k] = before
			}
		}
	}
	// W5: terminators (%, currency signs, ...) next to a European number join it.
	for k := 0; k < len(types); {
		if types[k] != bidi.ET {
			k++
			continue
		}
		end := k
		for end < len(types) && types[end] == bidi.ET {
			end++
		}
		if (k > 0 && types[k-1] == bidi.EN) || (end < len(types) && types[end] == bidi.EN) {
			for j := k; j < end; j++ {
				types[j] = bidi.EN
			}
		}
		k = end
	}
	// W6: remaining separators and terminators are neutral.
	// W7: a European number after left-to-right text is left-to-right.
	lastStrong = sos
	for k, c := range types {
		switch c {
		case bidi.ES, bidi.ET, bidi.CS:
			types[k] = bidi.ON
		case bidi.L, bidi.R:
			lastStrong = c
		case bidi.EN:
			if lastStrong == bidi.L {
				types[k] = bidi.L
			}
		}
	}

	// N0: paired brackets take the direction of the text they enclose.
	resolveBrackets(runes, idx, original, types, sos)

	// N1, N2: other neutrals take the direction of the text around them when
	// both sides agree (numbers count as right-to-left), else the paragraph's.
	strong := func(c bidi.Class) bidi.Class {
		if c == bidi.EN || c == bidi.AN {
			return bidi.R
		}
		return c
	}
	for k := 0; k < len(types); {
		if !isNeutral(types[k]) {
			k++
			continue
		}
		end := k
		for end < len(types) && isNeutral(types[end]) {
			end++
		}
		before, after := sos, sos
		if k > 0 {
			before = strong(types[k-1])
		}
		if end < len(types) {
			after = strong(types[end])
		}
		resolved := sos
		if before == after {
			resolved = before
		}
		for j := k; j < end; j++ {
			types[j] = resolved
		}
		k = end
	}

	// I1, I2.
	for k, i := range idx {
		level := paragraph
		switch c := types[k]; {
		case paragraph == 0 && c == bidi.R:
			level = 1
		case paragraph == 0 && (c == bidi.AN || c == bidi.EN):
			level = 2
		case paragraph == 1 && (c == bidi.L || c == bidi.EN || c == bidi.AN):
			level = 2
		}
		levels[i] = level
	}
	prev := paragraph
	for i, c := range original {
		if removedByX9(c) {
			levels[i] = prev
		}
		prev = levels[i]
	}

	// L1: whitespace before a separator or at the end of the line is reset to
	// the paragraph level.
	trailing := true
	for i := n - 1; i >= 0; i-- {
		switch c := original[i]; {
		case c == bidi.S || c == bidi.B:
			levels[i] = paragraph
			trailing = true
		case trailing && (c == bidi.WS || removedByX9(c)):
			levels[i] = paragraph
		default:
			trailing = false
		}
	}
	return levels
}

// removedByX9 reports whether rule X9 removes a character of class c; the
// explicit embedding, override and isolate controls are treated alike.
func removedByX9(c bidi.Class) bool {
	switch c {
	case bidi.BN, bidi.LRE, bidi.RLE, bidi.LRO, bidi.RLO, bidi.PDF,
		bidi.LRI, bidi.RLI, bidi.FSI, bidi.PDI:
		return true
	}
	return false
}

func isNeutral(c bidi.Class) bool {
	return c == bidi.ON || c == bidi.WS || c == bidi.S || c == bidi.B
}

// pairedBrackets maps opening brackets to their closing pair (the
// Bidi_Paired_Bracket property for the brackets likely in OCR text).
var pairedBrackets = map[rune]rune{
	'(': ')', '[': ']', '{': '}',
	'\u2045': '\u2046', '\u207D': '\u207E', '\u208D': '\u208E', // ⁅⁆ ⁽⁾ ₍₎
	'\u2329': '\u232A', '\u27E8': '\u27E9', '\u3008': '\u3009', '\u300A': '\u300B', // 〈〉 ⟨⟩ 〈〉 《》
	'\u300C': '\u300D', '\u300E': '\u300F', '\u3010': '\u3011', // 「」 『』 【】
	'\uFF08': '\uFF09', '\uFF3B': '\uFF3D', '\uFF5B': '\uFF5D', // （） ［］ ｛｝
}

// resolveBrackets applies rule N0 to types, the classes of runes[idx[k]].
func resolveBrackets(runes []rune, idx []int, original, types []bidi.Class, sos bidi.Class) {
	// BD16: pair brackets with a stack of openers.
	type pair struct{ open, close int }
	var pairs []pair
	var stack []int
	closing := make(map[rune]rune, len(pairedBrackets))
	for open, close := range pairedBrackets {
		closing[close] = open
	}
	for k, i := range idx {
		if types[k] != bidi.ON {
			continue
		}
		r := runes[i]
		if _, ok := pairedBrackets[r]; ok {
			if len(stack) == 63 {
				break
			}
			stack = append(stack, k)
			continue
		}
		if open, ok := closing[r]; ok {
			for s := len(stack) - 1; s >= 0; s-- {
				if runes[idx[stack[s]]] == open {
					pairs = append(pairs, pair{stack[s], k})
					stack = stack[:s]
					break
				}
			}
		}
	}
	sort.Slice(pairs, func(a, b int) bool { return pairs[a].open < pairs[b].open })

	direction := func(c bidi.Class) bidi.Class {
		switch c {
		case bidi.L:
			return bidi.L
		case bidi.R, bidi.EN, bidi.AN:
			return bidi.R
		}
		return bidi.ON
	}
	opposite := bidi.R
	if sos == bidi.R {
		opposite = bidi.L
	}
	for _, p := range pairs {
		found := bidi.ON
		for k := p.open + 1; k < p.close; k++ {
			if d := direction(types[k]); d == sos {
				found = sos
				break
			} else if d == opposite {
				found = opposite
			}
		}
		if found == bidi.ON {
			continue // N1 and N2 resolve brackets around neutral text
		}
		if found == opposite {
			// Opposite-direction text inside: the brackets follow it only when
			// the text before them runs that way too.
			before := sos
			for k := p.open - 1; k >= 0; k-- {
				if d := direction(types[k]); d != bidi.ON {
					before = d
					break
				}
			}
			if before != opposite {
				found = sos
			}
		}
		for _, k := range []int{p.open, p.close} {
			types[k] = found
			// Marks on a bracket follow it.
			for j := k + 1; j < len(types) && original[idx[j]] == bidi.NSM; j++ {
				types[j] = found
			}
		}
	}
}

// visualOrder returns runes, with the embedding levels bidiLevels resolved for
// them, in the left-to-right order in which they are displayed (rule L2).
func visualOrder(runes []rune, levels []uint8) []rune {
	if len(runes) == 0 {
		return nil
	}
	out := slices.Clone(runes)
	lv := slices.Clone(levels)
	highest, lowest := slices.Max(lv), slices.Min(lv)
	lowestOdd := lowest | 1
	for level := highest; level >= lowestOdd; level-- {
		for i := 0; i < len(out); {
			if lv[i] < level {
				i++
				continue
			}
			j := i
			for j < len(out) && lv[j] >= level {
				j++
			}
			slices.Reverse(out[i:j])
			slices.Reverse(lv[i:j])
			i = j
		}
	}
	return out
}
