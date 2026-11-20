package compliance

import (
	"fmt"
	"strings"
	"time"
)

// Document is the report engine's page layout for other modules' reports
// (the asset lifecycle summary): the same A4 pages, fonts, tables and footer
// as the compliance reports, without their sections.
type Document struct{ r *renderer }

// Column is one table column. Width is in millimetres; the content width of
// a page is 180. Align is "L", "C" or "R".
type Column struct {
	Title string
	Width float64
	Align string
}

// Tone colours a table cell.
type Tone int

const (
	ToneNone Tone = iota
	ToneGood
	ToneBad
	ToneWarn
	ToneMuted
)

// Cell is one table cell.
type Cell struct {
	Text string
	Tone Tone
	Bold bool
}

// NewDocument starts a document. at is printed as the document's own date,
// so the same input renders the same bytes.
func NewDocument(title, reportID, author string, at time.Time) *Document {
	return &Document{r: newRenderer(title, reportID, author, at)}
}

// Cover draws a title page: the title, the organisation, label/value lines
// and a closing note in italics.
func (d *Document) Cover(title, organization string, lines [][2]string, note string) {
	r := d.r
	r.pdf.AddPage()
	r.pdf.Ln(30)
	r.font("B", 24)
	r.pdf.SetTextColor(colHeader.r, colHeader.g, colHeader.b)
	r.pdf.MultiCell(r.cw, 11, r.t(title), "", "L", false)
	r.pdf.SetTextColor(0, 0, 0)
	r.font("", 14)
	if strings.TrimSpace(organization) == "" {
		organization = "(nama organisasi belum diatur)"
	}
	r.pdf.MultiCell(r.cw, 8, r.t(organization), "", "L", false)
	r.pdf.Ln(12)
	for _, l := range lines {
		r.kv(l[0], l[1])
	}
	if note != "" {
		r.pdf.Ln(16)
		r.font("I", 9)
		r.pdf.SetTextColor(90, 90, 90)
		r.pdf.MultiCell(r.cw, 4.6, r.t(note), "", "L", false)
		r.pdf.SetTextColor(0, 0, 0)
	}
}

// NewPage starts a page.
func (d *Document) NewPage() { d.r.pdf.AddPage() }

// Heading is a numbered section title.
func (d *Document) Heading(s string) { d.r.heading(s) }

// Sub is a subsection title. It keeps room for the first rows of what follows,
// so a title is never left alone at the foot of a page.
func (d *Document) Sub(s string) {
	d.r.ensure(35)
	d.r.sub(s)
}

// Para is a paragraph.
func (d *Document) Para(s string) { d.r.para(s) }

// KV is a label/value line.
func (d *Document) KV(k, v string) { d.r.kv(k, v) }

// Big is one line in large bold type, for a headline figure.
func (d *Document) Big(s string) {
	d.r.font("B", 16)
	d.r.pdf.CellFormat(d.r.cw, 9, d.r.t(s), "", 1, "L", false, 0, "")
}

// Table draws a table that repeats its header on each new page.
func (d *Document) Table(cols []Column, rows [][]Cell) {
	cs := make([]col, len(cols))
	for i, c := range cols {
		cs[i] = col{title: c.Title, w: c.Width, align: c.Align}
	}
	rs := make([][]cell, len(rows))
	for i, row := range rows {
		rs[i] = make([]cell, len(row))
		for j, c := range row {
			rs[i][j] = cell{text: c.Text, bold: c.Bold, fill: toneFill(c.Tone)}
		}
	}
	d.r.table(cs, rs)
}

func toneFill(t Tone) *rgb {
	switch t {
	case ToneGood:
		return colPass
	case ToneBad:
		return colFail
	case ToneWarn:
		return colUnknown
	case ToneMuted:
		return colNA
	}
	return nil
}

// Bars draws a horizontal bar chart: one row per label, the bar's length in
// proportion to the largest value, and the value's text after it.
func (d *Document) Bars(labels []string, values []float64, texts []string) {
	r := d.r
	maxV := 0.0
	for _, v := range values {
		if v > maxV {
			maxV = v
		}
	}
	const labelW, textW, rowH = 40.0, 40.0, 7.0
	barMax := r.cw - labelW - textW
	r.ensure(rowH*float64(len(values)) + 4)
	for i, v := range values {
		y := r.pdf.GetY()
		r.font("", 9)
		r.pdf.SetXY(r.lm, y)
		r.pdf.CellFormat(labelW, rowH, r.t(labels[i]), "", 0, "L", false, 0, "")
		w := 0.0
		if maxV > 0 {
			w = barMax * v / maxV
		}
		if w > 0 {
			r.pdf.SetFillColor(colHeader.r, colHeader.g, colHeader.b)
			r.pdf.Rect(r.lm+labelW, y+1.5, w, rowH-3, "F")
		}
		r.pdf.SetXY(r.lm+labelW+w+2, y)
		r.pdf.CellFormat(textW, rowH, r.t(texts[i]), "", 0, "L", false, 0, "")
		r.pdf.SetXY(r.lm, y+rowH)
	}
	r.pdf.Ln(3)
}

// Output renders the PDF.
func (d *Document) Output() ([]byte, error) { return d.r.output() }

// FormatTime is a WIB timestamp in Indonesian ("20 November 2026 14:05 WIB").
func FormatTime(t time.Time) string { return fmtTime(t) }

// FormatDate is a WIB date in Indonesian ("20 November 2026").
func FormatDate(t time.Time) string { return fmtDate(t) }

// Rupiah formats an amount as "Rp 12.500.000", rounded to the rupiah.
func Rupiah(v float64) string {
	neg := v < 0
	if neg {
		v = -v
	}
	s := fmt.Sprintf("%.0f", v)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-Rp " + b.String()
	}
	return "Rp " + b.String()
}
