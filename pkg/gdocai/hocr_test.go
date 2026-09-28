package gdocai

import (
	"testing"

	"cloud.google.com/go/documentai/apiv1/documentaipb"

	"github.com/gardar/ocrchestra/pkg/hocr"
)

func anchor(start, end int64) *documentaipb.Document_TextAnchor {
	return &documentaipb.Document_TextAnchor{
		TextSegments: []*documentaipb.Document_TextAnchor_TextSegment{{StartIndex: start, EndIndex: end}},
	}
}

// sidewaysPoly returns the box (x1,y1)-(x2,y2) as Document AI reports text
// that reads bottom to top: clockwise from the text's top-left corner, which
// is the page's bottom-left.
func sidewaysPoly(x1, y1, x2, y2 float32) *documentaipb.BoundingPoly {
	return &documentaipb.BoundingPoly{NormalizedVertices: []*documentaipb.NormalizedVertex{
		{X: x1, Y: y2}, {X: x1, Y: y1}, {X: x2, Y: y1}, {X: x2, Y: y2},
	}}
}

func TestConvertLineFromProtoSideways(t *testing.T) {
	const text = "Hello, world\n"
	layout := func(start, end int64, y1, y2 float32) *documentaipb.Document_Page_Layout {
		return &documentaipb.Document_Page_Layout{
			TextAnchor:   anchor(start, end),
			BoundingPoly: sidewaysPoly(0.1, y1, 0.15, y2),
			Orientation:  documentaipb.Document_Page_Layout_PAGE_LEFT,
		}
	}
	space := &documentaipb.Document_Page_Token_DetectedBreak{Type: documentaipb.Document_Page_Token_DetectedBreak_SPACE}
	page := &documentaipb.Document_Page{
		Dimension: &documentaipb.Document_Page_Dimension{Width: 1000, Height: 1000},
		Tokens: []*documentaipb.Document_Page_Token{
			{Layout: layout(0, 5, 0.6, 0.8)},                        // "Hello", no break: the comma follows
			{Layout: layout(5, 7, 0.58, 0.6), DetectedBreak: space}, // ", "
			{Layout: layout(7, 13, 0.3, 0.55), DetectedBreak: space},
		},
	}
	line := convertLineFromProto(&documentaipb.Document_Page_Line{Layout: layout(0, 13, 0.3, 0.8)}, page, text, 1, 0, 0, 0)

	if got := line.Metadata["textangle"]; got != "90" {
		t.Errorf("textangle = %q, want 90", got)
	}
	if want := (hocr.BoundingBox{X1: 100, Y1: 300, X2: 150, Y2: 800}); line.BBox != want {
		t.Errorf("line bbox = %+v, want %+v", line.BBox, want)
	}
	want := []struct {
		text    string
		noSpace bool
	}{{"Hello", true}, {",", false}, {"world", false}}
	if len(line.Words) != len(want) {
		t.Fatalf("got %d words, want %d", len(line.Words), len(want))
	}
	for i, w := range want {
		word := line.Words[i]
		if word.Text != w.text || (word.Metadata[hocr.NoSpaceAfter] != "") != w.noSpace {
			t.Errorf("word %d = %q (no space after: %v), want %q (%v)",
				i, word.Text, word.Metadata[hocr.NoSpaceAfter] != "", w.text, w.noSpace)
		}
		if word.BBox.X1 > word.BBox.X2 || word.BBox.Y1 > word.BBox.Y2 {
			t.Errorf("word %d bbox %+v is inverted", i, word.BBox)
		}
	}
}
