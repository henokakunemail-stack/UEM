package assetlicense

import (
	"fmt"
	"strings"
	"time"

	rpt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/compliance"
)

// Indonesian labels shared by the PDF. The console has its own translations.

func categoryLabel(c string) string {
	switch c {
	case CategoryLaptop:
		return "Laptop"
	case CategoryDesktop:
		return "Desktop"
	case CategoryServer:
		return "Server"
	}
	return "Lainnya"
}

func ageSourceLabel(s string) string {
	switch s {
	case AgeFromPurchase:
		return "dari tanggal beli"
	case AgeFromBIOS:
		return "perkiraan dari BIOS"
	case AgeFromOSInstall:
		return "perkiraan dari tanggal instal OS"
	}
	return "tidak diketahui"
}

func bucketLabel(k string) string {
	switch k {
	case "0-1":
		return "0-1 tahun"
	case "1-3":
		return "1-3 tahun"
	case "3-5":
		return "3-5 tahun"
	case "5+":
		return "5 tahun atau lebih"
	}
	return "Tidak diketahui"
}

func years(v float64) string {
	return strings.Replace(fmt.Sprintf("%.1f tahun", v), ".", ",", 1)
}

func unitName(u Unit) string {
	parts := []string{}
	if u.AssetTag != "" {
		parts = append(parts, u.AssetTag)
	}
	if u.Hostname != "" {
		parts = append(parts, u.Hostname)
	}
	if len(parts) == 0 {
		return u.DeviceID
	}
	return strings.Join(parts, " / ")
}

func unitAge(u Unit) string {
	if u.AgeYears == nil {
		return "Tidak diketahui"
	}
	return years(*u.AgeYears) + " (" + ageSourceLabel(u.AgeSource) + ")"
}

func costText(v *float64) string {
	if v == nil {
		return "belum diatur"
	}
	return rpt.Rupiah(*v)
}

func lineText(l BudgetLine) string {
	if l.UnitsWithoutCost > 0 {
		return fmt.Sprintf("%s (+%d unit tanpa biaya)", rpt.Rupiah(l.Cost), l.UnitsWithoutCost)
	}
	return rpt.Rupiah(l.Cost)
}

// LifecycleReportID names a summary by its generation time.
func LifecycleReportID(at time.Time) string {
	return "AL-" + at.In(lifecycleZone).Format("20060102-150405")
}

// RenderLifecyclePDF draws the management summary.
func RenderLifecyclePDF(rep LifecycleReport, reportID, organization, generatedBy string) ([]byte, error) {
	const title = "Ringkasan Siklus Hidup Aset"
	d := rpt.NewDocument(title, reportID, organization, rep.GeneratedAt)
	d.Cover(title, organization, [][2]string{
		{"Status data per", rpt.FormatTime(rep.GeneratedAt)},
		{"Dibuat oleh", generatedBy},
		{"Nomor laporan", reportID},
		{"Jumlah unit", fmt.Sprint(len(rep.Units))},
	}, "Ringkasan ini dihasilkan otomatis dari register aset dan data agen Endpoint Manager. Umur yang diambil "+
		"dari BIOS atau tanggal instal OS adalah perkiraan dan ditandai demikian. Angka anggaran adalah proyeksi, "+
		"bukan nilai buku akuntansi resmi.")

	// 1. Summary.
	d.NewPage()
	d.Heading("1. Ringkasan")
	d.Big(fmt.Sprintf("%d unit, %d sudah melewati umur pakai", len(rep.Units), rep.Overdue))
	d.Table([]rpt.Column{{Title: "Ukuran", Width: 110, Align: "L"}, {Title: "Nilai", Width: 70, Align: "R"}}, [][]rpt.Cell{
		{{Text: "Unit dalam armada"}, {Text: fmt.Sprint(len(rep.Units))}},
		{{Text: "Sudah melewati umur pakai", Tone: toneIf(rep.Overdue > 0, rpt.ToneBad)}, {Text: fmt.Sprint(rep.Overdue)}},
		{{Text: "Biaya penggantian unit yang sudah lewat umur"}, {Text: lineText(rep.Forecast.Overdue)}},
		{{Text: "Spesifikasi di bawah batas", Tone: toneIf(rep.BelowSpec > 0, rpt.ToneWarn)}, {Text: fmt.Sprint(rep.BelowSpec)}},
		{{Text: "Umur tidak diketahui (data kurang)", Tone: toneIf(rep.Forecast.MissingAge > 0, rpt.ToneWarn)}, {Text: fmt.Sprint(rep.Forecast.MissingAge)}},
		{{Text: fmt.Sprintf("Nilai pembelian (%d aset dengan tanggal dan harga beli)", rep.BookValue.Assets)}, {Text: rpt.Rupiah(rep.BookValue.PurchaseTotal)}},
		{{Text: "Nilai buku saat ini (garis lurus)"}, {Text: rpt.Rupiah(rep.BookValue.BookTotal)}},
		{{Text: "Seat lisensi tak dipakai"}, {Text: rpt.Rupiah(rep.UnusedSeatValue)}},
		{{Text: "Alokasi lisensi tanpa instalasi"}, {Text: rpt.Rupiah(rep.AllocatedNotInstValue)}},
	})
	if rep.BookValue.Excluded > 0 {
		d.Para(fmt.Sprintf("%d aset tidak punya tanggal beli atau harga beli, sehingga tidak masuk ke nilai pembelian "+
			"maupun nilai buku.", rep.BookValue.Excluded))
	}

	// 2. Age.
	d.Heading("2. Umur armada")
	var labels, texts []string
	var values []float64
	for _, b := range rep.AgeBuckets {
		labels = append(labels, bucketLabel(b.Key))
		values = append(values, float64(b.Count))
		texts = append(texts, fmt.Sprintf("%d unit", b.Count))
	}
	d.Bars(labels, values, texts)
	d.Sub("Sumber umur")
	d.Table([]rpt.Column{{Title: "Sumber", Width: 110, Align: "L"}, {Title: "Unit", Width: 30, Align: "R"}}, [][]rpt.Cell{
		{{Text: "Dari tanggal beli (register aset)", Tone: rpt.ToneGood}, {Text: fmt.Sprint(rep.AgeSources[AgeFromPurchase])}},
		{{Text: "Perkiraan dari tanggal BIOS", Tone: rpt.ToneWarn}, {Text: fmt.Sprint(rep.AgeSources[AgeFromBIOS])}},
		{{Text: "Perkiraan dari tanggal instal OS", Tone: rpt.ToneWarn}, {Text: fmt.Sprint(rep.AgeSources[AgeFromOSInstall])}},
		{{Text: "Tidak diketahui", Tone: rpt.ToneMuted}, {Text: fmt.Sprint(rep.AgeSources[AgeUnknown])}},
	})

	// 3. Forecast.
	d.Heading("3. Prediksi anggaran penggantian")
	d.Para("Setiap unit diganti satu kali, pada tanggal mulai pakainya ditambah umur pakai kategorinya. Biaya " +
		"memakai biaya pengganti kategori; bila belum diatur, harga beli aset; bila keduanya kosong, unit dihitung " +
		"tanpa biaya.")
	yrows := [][]rpt.Cell{{{Text: "Sudah lewat umur pakai (segera)", Bold: true}, {Text: fmt.Sprint(rep.Forecast.Overdue.Units)}, {Text: lineText(rep.Forecast.Overdue)}}}
	for _, y := range rep.Forecast.Years {
		label := y.Label
		if y.Start.Month() != time.January || y.End.Month() != time.January || y.End.Day() != 1 {
			label = fmt.Sprintf("%s (%s s.d. %s)", y.Label, rpt.FormatDate(y.Start), rpt.FormatDate(y.End.Add(-time.Second)))
		}
		yrows = append(yrows, []rpt.Cell{{Text: label}, {Text: fmt.Sprint(y.Units)}, {Text: lineText(y)}})
	}
	d.Sub("Per tahun")
	d.Table([]rpt.Column{{Title: "Periode", Width: 95, Align: "L"}, {Title: "Unit", Width: 20, Align: "R"}, {Title: "Biaya", Width: 65, Align: "R"}}, yrows)
	var qrows [][]rpt.Cell
	for _, q := range rep.Forecast.Quarters {
		qrows = append(qrows, []rpt.Cell{{Text: q.Label}, {Text: fmt.Sprint(q.Units)}, {Text: lineText(q)}})
	}
	d.Sub("Per kuartal")
	d.Table([]rpt.Column{{Title: "Kuartal", Width: 95, Align: "L"}, {Title: "Unit", Width: 20, Align: "R"}, {Title: "Biaya", Width: 65, Align: "R"}}, qrows)
	if rep.Forecast.MissingAge > 0 {
		d.Para(fmt.Sprintf("Data kurang: %d unit tidak punya tanggal beli, tanggal BIOS, maupun tanggal instal OS. "+
			"Unit tersebut tidak masuk ke prediksi di atas.", rep.Forecast.MissingAge))
	}

	// 4. Overdue units.
	d.Heading("4. Unit yang sudah melewati umur pakai")
	var orows [][]rpt.Cell
	for _, u := range rep.Units {
		if u.Overdue {
			orows = append(orows, []rpt.Cell{{Text: unitName(u)}, {Text: categoryLabel(u.Category)},
				{Text: unitAge(u)}, {Text: rpt.FormatDate(*u.DueDate)}, {Text: costText(u.ReplacementCost)}})
		}
	}
	if len(orows) == 0 {
		d.Para("Tidak ada.")
	} else {
		d.Table([]rpt.Column{{Title: "Unit", Width: 50, Align: "L"}, {Title: "Kategori", Width: 22, Align: "L"},
			{Title: "Umur", Width: 48, Align: "L"}, {Title: "Jatuh tempo", Width: 30, Align: "L"}, {Title: "Biaya pengganti", Width: 30, Align: "R"}}, orows)
	}

	// 5. Below spec.
	d.Heading("5. Spesifikasi di bawah batas")
	d.Para(specRule(rep.Settings))
	var srows [][]rpt.Cell
	for _, u := range rep.Units {
		if len(u.BelowSpec) > 0 {
			srows = append(srows, []rpt.Cell{{Text: unitName(u)}, {Text: ramText(u.RAMBytes)}, {Text: diskText(u.DiskFreePct)}, {Text: specIssues(u.BelowSpec)}})
		}
	}
	if len(srows) == 0 {
		d.Para("Tidak ada.")
	} else {
		d.Table([]rpt.Column{{Title: "Unit", Width: 60, Align: "L"}, {Title: "RAM", Width: 25, Align: "R"},
			{Title: "Disk kosong", Width: 30, Align: "R"}, {Title: "Masalah", Width: 65, Align: "L"}}, srows)
	}

	// 6. Licenses.
	d.Heading("6. Lisensi")
	d.Sub("Seat terbuang")
	var lrows [][]rpt.Cell
	for _, l := range rep.Licenses {
		if !l.Applicable {
			lrows = append(lrows, []rpt.Cell{{Text: l.SoftwareName}, {Text: fmt.Sprint(l.TotalSeats)}, {Text: fmt.Sprint(l.Allocated)},
				{Text: fmt.Sprint(l.Installed)}, {Text: "lisensi situs", Tone: rpt.ToneMuted}, {Text: "-"}})
			continue
		}
		lrows = append(lrows, []rpt.Cell{{Text: l.SoftwareName}, {Text: fmt.Sprint(l.TotalSeats)}, {Text: fmt.Sprint(l.Allocated)},
			{Text: fmt.Sprint(l.Installed)},
			{Text: fmt.Sprintf("%d seat (%s)", l.UnusedSeats, costText(l.UnusedValue)), Tone: toneIf(l.UnusedSeats > 0, rpt.ToneWarn)},
			{Text: allocText(l), Tone: toneIf(l.AllocatedNotInstalled > 0, rpt.ToneWarn)}})
	}
	if len(lrows) == 0 {
		d.Para("Belum ada lisensi yang tercatat.")
	} else {
		d.Table([]rpt.Column{{Title: "Software", Width: 45, Align: "L"}, {Title: "Seat", Width: 14, Align: "R"},
			{Title: "Dialokasikan", Width: 22, Align: "R"}, {Title: "Terpasang", Width: 19, Align: "R"},
			{Title: "Seat tak dipakai", Width: 40, Align: "L"}, {Title: "Alokasi tanpa instalasi", Width: 40, Align: "L"}}, lrows)
		d.Para("Seat tak dipakai = seat dibeli dikurangi perangkat yang dialokasikan atau terpasang (dihitung sekali per " +
			"perangkat). Nilai rupiah = biaya lisensi / jumlah seat x jumlah seat. Perangkat yang belum mengirim " +
			"inventory tidak dihitung sebagai tanpa instalasi.")
	}
	d.Sub(fmt.Sprintf("Lisensi yang habis dalam %d hari", ExpiryWindowDays))
	var erows [][]rpt.Cell
	for _, e := range rep.ExpiringLicenses {
		erows = append(erows, []rpt.Cell{{Text: e.SoftwareName}, {Text: rpt.FormatDate(e.ExpiresAt)},
			{Text: daysText(e.DaysLeft), Tone: toneIf(e.DaysLeft < 0, rpt.ToneBad)}, {Text: rpt.Rupiah(e.Cost)}})
	}
	if len(erows) == 0 {
		d.Para("Tidak ada.")
	} else {
		d.Table([]rpt.Column{{Title: "Software", Width: 70, Align: "L"}, {Title: "Berakhir", Width: 40, Align: "L"},
			{Title: "Sisa", Width: 35, Align: "L"}, {Title: "Biaya", Width: 35, Align: "R"}}, erows)
	}

	// 7. Warranties.
	d.Heading("7. Garansi")
	d.Para(fmt.Sprintf("%d aset garansinya sudah habis. Garansi yang habis dalam %d hari:", rep.WarrantiesExpired, ExpiryWindowDays))
	var wrows [][]rpt.Cell
	for _, w := range rep.ExpiringWarranties {
		name := w.AssetTag
		if w.Hostname != "" {
			name += " / " + w.Hostname
		}
		wrows = append(wrows, []rpt.Cell{{Text: name}, {Text: w.Model}, {Text: rpt.FormatDate(w.ExpiresAt)}, {Text: daysText(w.DaysLeft)}})
	}
	if len(wrows) == 0 {
		d.Para("Tidak ada.")
	} else {
		d.Table([]rpt.Column{{Title: "Aset", Width: 55, Align: "L"}, {Title: "Model", Width: 55, Align: "L"},
			{Title: "Berakhir", Width: 40, Align: "L"}, {Title: "Sisa", Width: 30, Align: "L"}}, wrows)
	}

	// 8. Method.
	d.Heading("8. Metodologi")
	var crows [][]rpt.Cell
	for _, c := range Categories {
		v := rep.Settings.Categories[c]
		cost := "belum diatur"
		if v.ReplacementCost > 0 {
			cost = rpt.Rupiah(v.ReplacementCost)
		}
		crows = append(crows, []rpt.Cell{{Text: categoryLabel(c)}, {Text: fmt.Sprintf("%d tahun", v.UsefulLifeYears)}, {Text: cost}})
	}
	d.Table([]rpt.Column{{Title: "Kategori", Width: 60, Align: "L"}, {Title: "Umur pakai", Width: 50, Align: "R"}, {Title: "Biaya pengganti", Width: 70, Align: "R"}}, crows)
	d.Para("Umur dihitung dari tanggal beli di register aset. Bila kosong, dari tanggal rilis BIOS yang dilaporkan " +
		"agen, lalu dari tanggal instal sistem operasi. Keduanya perkiraan: pembaruan BIOS atau instal ulang OS " +
		"membuat perangkat tampak lebih muda. Aset berkategori Lainnya (monitor, dock) tidak memakai data perangkat " +
		"yang ditautkan.")
	d.Para("Kategori diambil dari aset; bila kosong, dari jenis casing yang dilaporkan agen; bila keduanya tidak ada, " +
		"Lainnya. Nilai buku memakai metode garis lurus atas harga beli selama umur pakai kategori, hanya untuk aset " +
		"yang punya tanggal dan harga beli.")
	return d.Output()
}

func toneIf(cond bool, t rpt.Tone) rpt.Tone {
	if cond {
		return t
	}
	return rpt.ToneNone
}

func daysText(n int) string {
	if n < 0 {
		return fmt.Sprintf("lewat %d hari", -n)
	}
	return fmt.Sprintf("%d hari", n)
}

func allocText(l LicenseWaste) string {
	s := fmt.Sprintf("%d alokasi (%s)", l.AllocatedNotInstalled, costText(l.AllocatedNotInstalledValue))
	if l.AllocatedUnverified > 0 {
		s += fmt.Sprintf("; %d belum ada inventory", l.AllocatedUnverified)
	}
	return s
}

func ramText(b *int64) string {
	if b == nil {
		return "-"
	}
	return strings.Replace(fmt.Sprintf("%.1f GB", float64(*b)/(1<<30)), ".", ",", 1)
}

func diskText(p *float64) string {
	if p == nil {
		return "-"
	}
	return fmt.Sprintf("%.0f%%", *p)
}

func specIssues(s []string) string {
	var out []string
	for _, k := range s {
		switch k {
		case "ram":
			out = append(out, "RAM di bawah batas")
		case "disk":
			out = append(out, "disk hampir penuh")
		}
	}
	return strings.Join(out, ", ")
}

func specRule(s LifecycleSettings) string {
	var parts []string
	if s.MinRAMGB > 0 {
		parts = append(parts, fmt.Sprintf("RAM kurang dari %g GB", s.MinRAMGB))
	}
	if s.MinDiskFreePercent > 0 {
		parts = append(parts, fmt.Sprintf("ruang kosong disk kurang dari %g%%", s.MinDiskFreePercent))
	}
	if len(parts) == 0 {
		return "Pemeriksaan spesifikasi dimatikan di pengaturan."
	}
	return "Batas: " + strings.Join(parts, ", atau ") + ". Dibaca dari inventory terakhir setiap perangkat."
}
