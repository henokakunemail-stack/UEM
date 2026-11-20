package compliance

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/go-pdf/fpdf"
)

// reportZone is the time zone printed in reports. Fixed rather than loaded
// from the system database, which a Windows server may not have.
var reportZone = time.FixedZone("WIB", 7*3600)

var monthsID = [...]string{"Januari", "Februari", "Maret", "April", "Mei", "Juni", "Juli",
	"Agustus", "September", "Oktober", "November", "Desember"}

func fmtTime(t time.Time) string {
	t = t.In(reportZone)
	return fmt.Sprintf("%d %s %d %02d:%02d WIB", t.Day(), monthsID[t.Month()-1], t.Year(), t.Hour(), t.Minute())
}

func fmtDate(t time.Time) string {
	t = t.In(reportZone)
	return fmt.Sprintf("%d %s %d", t.Day(), monthsID[t.Month()-1], t.Year())
}

// PeriodLabel turns "2026-11" into "November 2026".
func PeriodLabel(period string) string {
	t, err := time.Parse("2006-01", period)
	if err != nil {
		return period
	}
	return fmt.Sprintf("%s %d", monthsID[t.Month()-1], t.Year())
}

// ControlLabel is a control's Indonesian name.
func ControlLabel(c string) string {
	switch c {
	case ControlDiskEncryption:
		return "Enkripsi disk"
	case ControlAntivirus:
		return "Antivirus"
	case ControlFirewall:
		return "Firewall"
	case ControlPatches:
		return "Patch keamanan"
	case ControlPasswordPolicy:
		return "Kebijakan password"
	case ControlScreenLock:
		return "Kunci layar"
	}
	return c
}

// controlShort is a control's name narrow enough for an appendix column.
func controlShort(c string) string {
	switch c {
	case ControlDiskEncryption:
		return "Enkripsi"
	case ControlPatches:
		return "Patch"
	case ControlPasswordPolicy:
		return "Password"
	}
	return ControlLabel(c)
}

func deviceStatusLabel(s string) string {
	switch s {
	case DeviceCompliant:
		return "Patuh"
	case DeviceNonCompliant:
		return "Tidak patuh"
	case DeviceNotApplicable:
		return "Tidak berlaku"
	}
	return "Tidak diketahui"
}

func controlCell(s string) string {
	switch s {
	case StatusPass:
		return "Lulus"
	case StatusFail:
		return "Gagal"
	case StatusNotApplicable:
		return "N/B"
	}
	return "?"
}

type rgb struct{ r, g, b int }

var (
	colPass    = &rgb{220, 242, 226}
	colFail    = &rgb{250, 219, 216}
	colUnknown = &rgb{253, 240, 205}
	colNA      = &rgb{235, 236, 238}
	colHeader  = &rgb{38, 70, 110}
)

func statusFill(s string) *rgb {
	switch s {
	case StatusPass, DeviceCompliant:
		return colPass
	case StatusFail, DeviceNonCompliant:
		return colFail
	case StatusNotApplicable: // same value as DeviceNotApplicable
		return colNA
	}
	return colUnknown
}

func pct(p *float64) string {
	if p == nil {
		return "-"
	}
	return fmt.Sprintf("%.0f%%", *p)
}

func trend(cur, prev *float64) string {
	if cur == nil || prev == nil {
		return "-"
	}
	d := *cur - *prev
	switch {
	case d > 0.5:
		return fmt.Sprintf("naik %.0f poin", d)
	case d < -0.5:
		return fmt.Sprintf("turun %.0f poin", -d)
	}
	return "tetap"
}

type cell struct {
	text string
	fill *rgb
	bold bool
}

type col struct {
	title string
	w     float64
	align string
}

type renderer struct {
	pdf *fpdf.Fpdf
	tr  func(string) string
	lm  float64
	cw  float64 // content width
	ph  float64
	bm  float64
}

// pdfSafe maps characters outside the Windows-1252 core fonts to ASCII so they
// do not print as garbage.
var pdfSafe = strings.NewReplacer("≥", ">=", "≤", "<=", "→", "->", "✓", "v", "✗", "x")

func (r *renderer) t(s string) string { return r.tr(pdfSafe.Replace(s)) }

func (r *renderer) font(style string, size float64) { r.pdf.SetFont("Helvetica", style, size) }

func (r *renderer) heading(s string) {
	r.ensure(20)
	r.pdf.Ln(2)
	r.font("B", 14)
	r.pdf.SetTextColor(colHeader.r, colHeader.g, colHeader.b)
	r.pdf.CellFormat(r.cw, 8, r.t(s), "", 1, "L", false, 0, "")
	r.pdf.SetDrawColor(colHeader.r, colHeader.g, colHeader.b)
	y := r.pdf.GetY()
	r.pdf.Line(r.lm, y, r.lm+r.cw, y)
	r.pdf.SetDrawColor(180, 180, 180)
	r.pdf.SetTextColor(0, 0, 0)
	r.pdf.Ln(3)
}

func (r *renderer) sub(s string) {
	r.ensure(14)
	r.font("B", 11)
	r.pdf.CellFormat(r.cw, 6, r.t(s), "", 1, "L", false, 0, "")
	r.pdf.Ln(1)
}

func (r *renderer) para(s string) {
	r.font("", 9.5)
	r.pdf.MultiCell(r.cw, 4.6, r.t(s), "", "L", false)
	r.pdf.Ln(1.5)
}

func (r *renderer) kv(k, v string) {
	r.font("B", 10)
	r.pdf.CellFormat(45, 6, r.t(k), "", 0, "L", false, 0, "")
	r.font("", 10)
	r.pdf.MultiCell(r.cw-45, 6, r.t(v), "", "L", false)
}

// ensure starts a new page when fewer than h mm remain.
func (r *renderer) ensure(h float64) {
	if r.pdf.GetY()+h > r.ph-r.bm {
		r.pdf.AddPage()
	}
}

const lineH = 4.2

func (r *renderer) header(cols []col) {
	r.font("B", 8)
	r.pdf.SetFillColor(colHeader.r, colHeader.g, colHeader.b)
	r.pdf.SetTextColor(255, 255, 255)
	x, y := r.lm, r.pdf.GetY()
	h := 0.0
	for _, c := range cols {
		if n := float64(len(r.pdf.SplitText(r.t(c.title), c.w-2))); n*lineH+2 > h {
			h = n*lineH + 2
		}
	}
	for _, c := range cols {
		r.pdf.Rect(x, y, c.w, h, "F")
		r.pdf.SetXY(x+1, y+1)
		for _, ln := range r.pdf.SplitText(r.t(c.title), c.w-2) {
			r.pdf.CellFormat(c.w-2, lineH, ln, "", 2, c.align, false, 0, "")
		}
		x += c.w
	}
	r.pdf.SetTextColor(0, 0, 0)
	r.pdf.SetXY(r.lm, y+h)
}

const maxCellLines = 40

func (r *renderer) table(cols []col, rows [][]cell) {
	r.ensure(20)
	r.header(cols)
	for _, row := range rows {
		lines := make([][]string, len(cols))
		h := 0.0
		for i, c := range cols {
			if i >= len(row) {
				continue
			}
			r.font(styleOf(row[i]), 8.5)
			ls := r.pdf.SplitText(r.t(row[i].text), c.w-2)
			if len(ls) > maxCellLines {
				ls = append(ls[:maxCellLines-1], "...")
			}
			lines[i] = ls
			if hh := float64(len(ls))*lineH + 1.6; hh > h {
				h = hh
			}
		}
		if h < lineH+1.6 {
			h = lineH + 1.6
		}
		if r.pdf.GetY()+h > r.ph-r.bm {
			r.pdf.AddPage()
			r.header(cols)
		}
		x, y := r.lm, r.pdf.GetY()
		for i, c := range cols {
			style := "D"
			if i < len(row) && row[i].fill != nil {
				f := row[i].fill
				r.pdf.SetFillColor(f.r, f.g, f.b)
				style = "FD"
			}
			r.pdf.Rect(x, y, c.w, h, style)
			if i < len(row) {
				r.font(styleOf(row[i]), 8.5)
				r.pdf.SetXY(x+1, y+0.8)
				for _, ln := range lines[i] {
					r.pdf.CellFormat(c.w-2, lineH, ln, "", 2, c.align, false, 0, "")
				}
			}
			x += c.w
		}
		r.pdf.SetXY(r.lm, y+h)
	}
	r.pdf.Ln(3)
}

func styleOf(c cell) string {
	if c.bold {
		return "B"
	}
	return ""
}

// RenderPDF draws the snapshot. contentHash is printed on the verification
// page. The output is deterministic for a given snapshot: the PDF's own dates
// are the generation time, not the time of rendering.
func RenderPDF(s Snapshot, contentHash string) ([]byte, error) {
	r := newRenderer("Laporan Kepatuhan Perangkat", s.ReportID, s.Organization, s.GeneratedAt)
	r.cover(s)
	r.executive(s)
	r.exceptions(s)
	r.appendix(s)
	r.methodology(s)
	r.verification("5", s.ReportID, s.GeneratedAt, contentHash)
	return r.output()
}

// newRenderer sets up an A4 document whose own dates are the generation time,
// so rendering the same snapshot twice gives the same bytes.
func newRenderer(title, reportID, author string, at time.Time) *renderer {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(15, 15, 15)
	pdf.SetAutoPageBreak(true, 18)
	pdf.SetCatalogSort(true)
	pdf.SetCreationDate(at)
	pdf.SetModificationDate(at)
	pdf.SetCompression(true)
	pdf.SetTitle(title+" "+reportID, true)
	pdf.SetAuthor(author, true)
	pdf.SetCreator("Endpoint Manager", true)
	pdf.SetProducer("Endpoint Manager", true)
	pdf.AliasNbPages("")
	tr := pdf.UnicodeTranslatorFromDescriptor("cp1252")
	pw, ph := pdf.GetPageSize()
	lm, _, rm, _ := pdf.GetMargins()
	r := &renderer{pdf: pdf, tr: tr, lm: lm, cw: pw - lm - rm, ph: ph, bm: 18}
	pdf.SetDrawColor(180, 180, 180)
	pdf.SetLineWidth(0.2)
	pdf.SetFooterFunc(func() {
		pdf.SetY(-12)
		pdf.SetFont("Helvetica", "", 8)
		pdf.SetTextColor(110, 110, 110)
		pdf.CellFormat(r.cw/2, 5, r.t(title+" · "+reportID), "", 0, "L", false, 0, "")
		pdf.CellFormat(r.cw/2, 5, r.t(fmt.Sprintf("Halaman %d dari {nb}", pdf.PageNo())), "", 0, "R", false, 0, "")
		pdf.SetTextColor(0, 0, 0)
	})
	return r
}

func (r *renderer) output() ([]byte, error) {
	var buf bytes.Buffer
	if err := r.pdf.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (r *renderer) cover(s Snapshot) {
	r.pdf.AddPage()
	r.pdf.Ln(30)
	r.font("B", 24)
	r.pdf.SetTextColor(colHeader.r, colHeader.g, colHeader.b)
	r.pdf.MultiCell(r.cw, 11, r.t("Laporan Kepatuhan Perangkat"), "", "L", false)
	r.pdf.SetTextColor(0, 0, 0)
	r.font("", 14)
	org := s.Organization
	if strings.TrimSpace(org) == "" {
		org = "(nama organisasi belum diatur)"
	}
	r.pdf.MultiCell(r.cw, 8, r.t(org), "", "L", false)
	r.pdf.Ln(12)

	r.kv("Periode", PeriodLabel(s.Period))
	r.kv("Status data per", fmtTime(s.GeneratedAt))
	r.kv("Profil kepatuhan", s.ProfileName)
	r.kv("Cakupan perangkat", s.Scope)
	r.kv("Dibuat oleh", s.GeneratedBy)
	kind := "Manual"
	if s.Kind == KindScheduled {
		kind = "Terjadwal (bulanan)"
	}
	r.kv("Jenis laporan", kind)
	r.kv("Nomor laporan", s.ReportID)
	r.pdf.Ln(16)
	r.font("I", 9)
	r.pdf.SetTextColor(90, 90, 90)
	r.pdf.MultiCell(r.cw, 4.6, r.t("Laporan ini adalah paket bukti teknis yang dihasilkan otomatis dari data agen "+
		"Endpoint Manager. Laporan ini bukan sertifikasi dan bukan opini hukum. Keaslian file dapat diperiksa "+
		"dengan langkah di halaman terakhir."), "", "L", false)
	r.pdf.SetTextColor(0, 0, 0)
}

func (r *renderer) executive(s Snapshot) {
	r.pdf.AddPage()
	r.heading("1. Ringkasan eksekutif")
	sum := s.Summary
	evaluated := sum.Compliant + sum.NonCompliant
	r.font("B", 16)
	score := "Skor kepatuhan armada: " + pct(sum.Score)
	r.pdf.CellFormat(r.cw, 9, r.t(score), "", 1, "L", false, 0, "")
	r.font("", 10)
	if sum.Score != nil {
		r.pdf.MultiCell(r.cw, 5, r.t(fmt.Sprintf("%d dari %d perangkat yang dapat dievaluasi dinyatakan patuh. "+
			"Skor hanya dihitung dari perangkat yang datanya lengkap; %d perangkat yang tidak dapat dievaluasi "+
			"dicantumkan terpisah dan tidak dihitung sebagai patuh maupun tidak patuh.",
			sum.Compliant, evaluated, sum.Unknown)), "", "L", false)
	} else {
		r.pdf.MultiCell(r.cw, 5, r.t("Belum ada perangkat yang dapat dievaluasi, sehingga skor tidak dapat dihitung."), "", "L", false)
	}
	if s.Previous != nil {
		r.pdf.MultiCell(r.cw, 5, r.t(fmt.Sprintf("Dibanding laporan sebelumnya (%s, %s): skor %s.",
			s.Previous.ReportID, fmtDate(s.Previous.GeneratedAt), trend(sum.Score, s.Previous.Summary.Score))), "", "L", false)
	} else {
		r.pdf.MultiCell(r.cw, 5, r.t("Belum ada laporan sebelumnya untuk profil ini, sehingga tren belum tersedia."), "", "L", false)
	}
	r.pdf.Ln(3)

	r.sub("Status perangkat")
	r.table([]col{{"Status", 90, "L"}, {"Jumlah perangkat", 40, "R"}}, [][]cell{
		{{text: "Patuh", fill: colPass}, {text: fmt.Sprint(sum.Compliant)}},
		{{text: "Tidak patuh", fill: colFail}, {text: fmt.Sprint(sum.NonCompliant)}},
		{{text: "Tidak diketahui (data kurang atau usang)", fill: colUnknown}, {text: fmt.Sprint(sum.Unknown)}},
		{{text: "Tidak berlaku", fill: colNA}, {text: fmt.Sprint(sum.NotApplicable)}},
		{{text: "Total", bold: true}, {text: fmt.Sprint(sum.Devices), bold: true}},
	})

	r.sub("Kepatuhan per kontrol")
	prev := map[string]*float64{}
	if s.Previous != nil {
		for _, c := range s.Previous.Summary.Controls {
			prev[c.Control] = c.PassPercent
		}
	}
	var rows [][]cell
	for _, c := range sum.Controls {
		rows = append(rows, []cell{
			{text: ControlLabel(c.Control), bold: true}, {text: fmt.Sprint(c.Pass)}, {text: fmt.Sprint(c.Fail)},
			{text: fmt.Sprint(c.Unknown)}, {text: fmt.Sprint(c.NotApplicable)}, {text: pct(c.PassPercent), bold: true},
			{text: trend(c.PassPercent, prev[c.Control])},
		})
	}
	r.table([]col{{"Kontrol", 42, "L"}, {"Lulus", 18, "R"}, {"Gagal", 18, "R"}, {"Tidak diketahui", 24, "R"},
		{"Tidak berlaku", 22, "R"}, {"% lulus", 20, "R"}, {"Tren", 36, "L"}}, rows)
	r.para("% lulus = lulus / (lulus + gagal). Perangkat yang tidak diketahui atau tidak berlaku untuk suatu " +
		"kontrol tidak masuk ke persentase kontrol tersebut.")
}

func (r *renderer) exceptions(s Snapshot) {
	r.pdf.AddPage()
	r.heading("2. Daftar pengecualian")
	var non, unk []DeviceResult
	for _, d := range s.Devices {
		switch d.Status {
		case DeviceNonCompliant:
			non = append(non, d)
		case DeviceUnknown:
			unk = append(unk, d)
		}
	}

	r.sub(fmt.Sprintf("Perangkat tidak patuh (%d)", len(non)))
	if len(non) == 0 {
		r.para("Tidak ada perangkat yang tidak patuh.")
	} else {
		var rows [][]cell
		for _, d := range non {
			for _, c := range d.Controls {
				if c.Status != StatusFail {
					continue
				}
				text := c.Reason
				if len(c.Evidence) > 0 {
					text += "\nBukti: " + strings.Join(c.Evidence, "; ")
				}
				rows = append(rows, []cell{{text: d.Device.Hostname, bold: true}, {text: orDash(d.Device.Site)},
					{text: ControlLabel(c.Control)}, {text: text}})
			}
		}
		r.table([]col{{"Perangkat", 36, "L"}, {"Site", 24, "L"}, {"Kontrol", 30, "L"}, {"Alasan dan bukti", 90, "L"}}, rows)
	}

	r.sub(fmt.Sprintf("Perangkat yang tidak dapat dievaluasi (%d)", len(unk)))
	if len(unk) == 0 {
		r.para("Semua perangkat dalam cakupan dapat dievaluasi.")
		return
	}
	r.para("Perangkat ini tidak dihitung sebagai patuh. Penyebab paling umum: agen versi lama yang belum " +
		"melaporkan postur keamanan, perangkat lama offline, atau scan patch yang belum berjalan.")
	// One row per cause: an old agent leaves five posture controls unknown
	// for the same reason, and forty such devices should read as one line.
	type group struct {
		reason   string
		controls []string
		hosts    []string
		seenC    map[string]bool
		seenH    map[string]bool
	}
	groups := map[string]*group{}
	for _, d := range unk {
		for _, c := range d.Controls {
			if c.Status != StatusUnknown {
				continue
			}
			g := groups[c.Reason]
			if g == nil {
				g = &group{reason: c.Reason, seenC: map[string]bool{}, seenH: map[string]bool{}}
				groups[c.Reason] = g
			}
			if !g.seenC[c.Control] {
				g.seenC[c.Control] = true
				g.controls = append(g.controls, ControlLabel(c.Control))
			}
			if !g.seenH[d.Device.ID] {
				g.seenH[d.Device.ID] = true
				g.hosts = append(g.hosts, d.Device.Hostname)
			}
		}
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if len(groups[keys[i]].hosts) != len(groups[keys[j]].hosts) {
			return len(groups[keys[i]].hosts) > len(groups[keys[j]].hosts)
		}
		return keys[i] < keys[j]
	})
	var rows [][]cell
	for _, k := range keys {
		g := groups[k]
		rows = append(rows, []cell{{text: g.reason}, {text: strings.Join(g.controls, ", ")},
			{text: fmt.Sprint(len(g.hosts)), bold: true}, {text: strings.Join(g.hosts, ", ")}})
	}
	r.table([]col{{"Penyebab", 58, "L"}, {"Kontrol", 40, "L"}, {"Jumlah", 17, "R"}, {"Perangkat", 65, "L"}}, rows)
}

func (r *renderer) appendix(s Snapshot) {
	r.pdf.AddPage()
	r.heading("3. Lampiran: status per perangkat")
	controls := make([]string, 0, len(s.Summary.Controls))
	for _, c := range s.Summary.Controls {
		controls = append(controls, c.Control)
	}
	cols := []col{{"Perangkat", 34, "L"}, {"Site", 22, "L"}, {"Status", 20, "L"}}
	rest := r.cw - 76
	w := rest
	if len(controls) > 0 {
		w = rest / float64(len(controls))
	}
	for _, c := range controls {
		cols = append(cols, col{controlShort(c), w, "C"})
	}
	var rows [][]cell
	for _, d := range s.Devices {
		row := []cell{{text: d.Device.Hostname, bold: true}, {text: orDash(d.Device.Site)},
			{text: deviceStatusLabel(d.Status), fill: statusFill(d.Status)}}
		for _, c := range controls {
			cr, ok := d.Control(c)
			if !ok {
				row = append(row, cell{text: "-"})
				continue
			}
			row = append(row, cell{text: controlCell(cr.Status), fill: statusFill(cr.Status)})
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		r.para("Tidak ada perangkat dalam cakupan profil ini.")
		return
	}
	r.table(cols, rows)
	r.para("Keterangan: Lulus = memenuhi profil; Gagal = tidak memenuhi; ? = tidak diketahui (data tidak terbaca, " +
		"usang, atau belum dilaporkan); N/B = tidak berlaku untuk perangkat ini.")
}

func (r *renderer) methodology(s Snapshot) {
	r.pdf.AddPage()
	r.heading("4. Metodologi dan keterbatasan")
	r.sub("Sumber data")
	r.para("Enkripsi disk, antivirus, firewall, kebijakan password, dan kunci layar dibaca oleh agen Endpoint " +
		"Manager yang berjalan sebagai layanan sistem di setiap perangkat, lalu dikirim bersama laporan " +
		"inventory (otomatis tiap 4 jam, atau saat diminta dari console). Di Windows: BitLocker dari WMI " +
		"Win32_EncryptableVolume; antivirus dari Windows Security Center dan Microsoft Defender; firewall dari " +
		"profil Domain/Private/Public yang aktif; kebijakan password dari kebijakan akun lokal; kunci layar dari " +
		"pengaturan \"Machine inactivity limit\".")
	r.para("Status patch berasal dari scan Windows Update yang dijalankan agen secara terjadwal. Yang dihitung " +
		"hanya update kategori keamanan atau kritis, atau dengan tingkat keparahan kritis. Umur update dihitung " +
		"sejak scan pertama kali menemukannya hilang di perangkat tersebut.")

	r.sub("Aturan penilaian")
	r.para("Setiap kontrol dinilai Lulus, Gagal, Tidak diketahui, atau Tidak berlaku. Perangkat dinyatakan " +
		"Tidak patuh bila ada satu kontrol yang gagal; Tidak diketahui bila tidak ada yang gagal tetapi ada " +
		"kontrol yang tidak dapat dinilai; Patuh hanya bila semua kontrol yang berlaku lulus. Data yang gagal " +
		"dibaca tidak pernah dianggap lulus.")
	r.sub("Ambang profil \"" + s.ProfileName + "\"")
	var rows [][]cell
	for _, line := range describeRules(s.Rules) {
		rows = append(rows, []cell{{text: line[0], bold: true}, {text: line[1]}})
	}
	r.table([]col{{"Kontrol", 45, "L"}, {"Syarat", 135, "L"}}, rows)

	r.sub("Data yang dipakai")
	var noPosture, offline, stale int
	var oldest, newest *time.Time
	for _, d := range s.Devices {
		if !d.Device.Online {
			offline++
		}
		if d.Device.PostureCollectedAt == nil {
			noPosture++
			continue
		}
		t := *d.Device.PostureCollectedAt
		if oldest == nil || t.Before(*oldest) {
			oldest = &t
		}
		if newest == nil || t.After(*newest) {
			newest = &t
		}
		if s.Rules.MaxPostureAgeDays > 0 && daysBetween(t, s.GeneratedAt) > s.Rules.MaxPostureAgeDays {
			stale++
		}
	}
	lines := []string{
		fmt.Sprintf("Perangkat dalam cakupan: %d (offline saat laporan dibuat: %d).", len(s.Devices), offline),
		fmt.Sprintf("Perangkat tanpa data postur (agen lama atau belum melapor): %d.", noPosture),
		fmt.Sprintf("Perangkat dengan data postur lebih lama dari %d hari: %d.", s.Rules.MaxPostureAgeDays, stale),
	}
	if oldest != nil {
		lines = append(lines, fmt.Sprintf("Data postur tertua: %s; terbaru: %s.", fmtTime(*oldest), fmtTime(*newest)))
	}
	for _, l := range lines {
		r.para(l)
	}
	r.sub("Keterbatasan")
	r.para("Laporan ini adalah potret pada waktu pembuatannya, bukan pemantauan terus-menerus sepanjang periode. " +
		"Kunci layar yang dinilai hanya batas inaktivitas tingkat mesin; pengaturan screensaver per pengguna tidak " +
		"dibaca. Pengumpulan postur Linux dan macOS belum tersedia, sehingga perangkat tersebut tampil sebagai " +
		"tidak diketahui untuk kontrol postur.")
}

func describeRules(r Rules) [][2]string {
	var out [][2]string
	add := func(on bool, c, txt string) {
		if on {
			out = append(out, [2]string{ControlLabel(c), txt})
		} else {
			out = append(out, [2]string{ControlLabel(c), "Tidak dinilai dalam profil ini"})
		}
	}
	add(r.RequireDiskEncryption, ControlDiskEncryption, "Volume sistem terenkripsi penuh dan proteksi aktif")
	av := "Minimal satu antivirus aktif"
	if r.RequireRealTime {
		av += ", perlindungan real-time menyala"
	}
	if r.MaxSignatureAgeDays > 0 {
		av += fmt.Sprintf(", signature paling lama %d hari", r.MaxSignatureAgeDays)
	}
	add(r.RequireAntivirus, ControlAntivirus, av)
	add(r.RequireFirewallAllProfiles, ControlFirewall, "Firewall aktif di semua profil")
	pt := fmt.Sprintf("Tidak ada update keamanan/kritis yang hilang lebih dari %d hari", r.MaxMissingPatchAgeDays)
	if r.MaxPatchScanAgeDays > 0 {
		pt += fmt.Sprintf("; scan patch terakhir paling lama %d hari", r.MaxPatchScanAgeDays)
	}
	add(r.RequirePatches, ControlPatches, pt)
	var pw []string
	if r.MinPasswordLength > 0 {
		pw = append(pw, fmt.Sprintf("panjang minimum >= %d karakter", r.MinPasswordLength))
	}
	if r.MaxLockoutThreshold > 0 {
		pw = append(pw, fmt.Sprintf("akun terkunci setelah paling banyak %d percobaan salah", r.MaxLockoutThreshold))
	}
	if len(pw) == 0 {
		pw = append(pw, "kebijakan terbaca")
	}
	add(r.RequirePasswordPolicy, ControlPasswordPolicy, capFirst(strings.Join(pw, "; ")))
	add(r.RequireScreenLock, ControlScreenLock, fmt.Sprintf("Layar terkunci otomatis paling lama %d menit tidak aktif", r.MaxScreenLockMinutes))
	if r.MaxPostureAgeDays > 0 {
		out = append(out, [2]string{"Kesegaran data", fmt.Sprintf("Data postur lebih lama dari %d hari dinilai tidak diketahui", r.MaxPostureAgeDays)})
	}
	return out
}

func (r *renderer) verification(section, reportID string, at time.Time, contentHash string) {
	r.pdf.AddPage()
	r.heading(section + ". Verifikasi keaslian")
	r.kv("Nomor laporan", reportID)
	r.kv("Dibuat", fmtTime(at))
	r.font("B", 10)
	r.pdf.CellFormat(r.cw, 6, r.t("SHA-256 isi laporan"), "", 1, "L", false, 0, "")
	r.pdf.SetFont("Courier", "", 9.5)
	r.pdf.MultiCell(r.cw, 5, contentHash, "1", "L", false)
	r.pdf.Ln(4)
	r.para("Saat laporan ini dibuat, server mencatat dua sidik jari ke audit log yang berantai hash " +
		"(tamper-evident, aksi \"compliance.report.generated\") dan ke arsip laporan: SHA-256 isi laporan di atas, " +
		"dan SHA-256 file PDF ini. Sebuah file tidak dapat memuat sidik jarinya sendiri, karena itu SHA-256 file " +
		"tidak tercetak di halaman ini.")
	r.sub("Cara memeriksa")
	r.para("1. Hitung SHA-256 file PDF yang Anda terima. Windows: certutil -hashfile <nama-file>.pdf SHA256. " +
		"Linux/macOS: sha256sum <nama-file>.pdf.")
	r.para("2. Buka console Endpoint Manager, menu Kepatuhan, bagian Verifikasi laporan, lalu pilih file " +
		"tersebut atau tempel hash-nya. Console menampilkan laporan yang cocok beserta pembuat dan waktunya.")
	r.para("3. Bila tidak ada laporan yang cocok, file tersebut bukan file asli yang dibuat server: satu byte " +
		"yang berubah sudah menghasilkan hash yang sama sekali berbeda.")
}
