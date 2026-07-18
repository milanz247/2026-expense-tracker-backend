package report

import (
	"bytes"
	"fmt"
	"net/http"
	"strconv"

	"github.com/jung-kurt/gofpdf"
)

// Layout constants for the PDF statement — A4 portrait, 15mm margins on
// every side, giving a 180mm usable width that the seven table columns
// below sum to exactly.
const (
	pdfPageHeightMM = 297.0
	pdfMarginMM     = 15.0
	pdfUsableWidth  = 210.0 - 2*pdfMarginMM // 180

	// pdfBottomLimit is how far down the page a new row is allowed to
	// start before triggering a page break — kept a few mm above the
	// footer (drawn at pdfPageHeightMM-15) so the two never visually
	// collide.
	pdfBottomLimit = pdfPageHeightMM - 20

	pdfHeaderRowHeight = 7.0
	pdfDataRowHeight   = 6.0
)

// pdfColumn describes one ledger table column: its header label, its
// width in mm, and how its cells align.
type pdfColumn struct {
	Label string
	Width float64
	Align string
}

// pdfColumns' widths sum to exactly pdfUsableWidth (22+46+26+28+26+20+12 = 180).
var pdfColumns = []pdfColumn{
	{"Date", 22, "L"},
	{"Description", 46, "L"},
	{"Type", 26, "L"},
	{"Category", 28, "L"},
	{"Wallet", 26, "L"},
	{"Amount", 20, "R"},
	{"Fee", 12, "R"},
}

// writePDFExport renders rows as a minimalist, black-and-white,
// print-ready financial statement and writes it to w as an attachment
// download. Like writeCSVExport, the whole document is built into an
// in-memory buffer first so a failed render never reaches the client as
// a truncated file.
func writePDFExport(w http.ResponseWriter, rows []exportRow, summary exportSummary, dateRangeLabel string) error {
	pdf := gofpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(pdfMarginMM, pdfMarginMM, pdfMarginMM)
	// Page breaks are managed by hand (see the row loop below) so the
	// table header can be redrawn at the top of every new page — gofpdf's
	// automatic page-break has no concept of a repeating table header.
	pdf.SetAutoPageBreak(false, 0)
	pdf.AliasNbPages("{nb}")
	// gofpdf's core Helvetica/Courier fonts only cover the cp1252 code
	// page — not full Unicode — so every piece of text drawn below runs
	// through this translator first. Latin-script text (the vast
	// majority of descriptions/category/wallet names) round-trips
	// perfectly; a rune outside cp1252 (e.g. Sinhala, emoji) is dropped
	// rather than mis-rendered as mojibake. The CSV export has no such
	// limitation — it's plain UTF-8 — so it remains the fully accurate
	// export path for any non-Latin data.
	tr := pdf.UnicodeTranslatorFromDescriptor("")
	pdf.SetFooterFunc(func() { drawPDFFooter(pdf, tr) })

	pdf.AddPage()
	drawPDFHeader(pdf, tr(dateRangeLabel))
	drawPDFSummaryBoxes(pdf, summary)
	drawPDFTableHeader(pdf)

	if len(rows) == 0 {
		pdf.SetFont("helvetica", "I", 9)
		pdf.SetTextColor(130, 130, 130)
		pdf.CellFormat(pdfUsableWidth, 10, "No transactions match the selected filters.", "LRB", 1, "C", false, 0, "")
	} else {
		for i, row := range rows {
			if pdf.GetY()+pdfDataRowHeight > pdfBottomLimit {
				pdf.AddPage()
				drawPDFTableHeader(pdf)
			}
			drawPDFRow(pdf, tr, row, i%2 == 1)
		}
	}

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return err
	}

	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `attachment; filename="financial_report.pdf"`)
	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
	return nil
}

// drawPDFHeader draws the "FINANCIAL STATEMENT" title, the resolved
// date-range subtitle, and a single black rule beneath — the whole of
// this app's black & white statement header, deliberately with no
// color, icons, or logo to keep it print-ready.
func drawPDFHeader(pdf *gofpdf.Fpdf, dateRangeLabel string) {
	pdf.SetFont("helvetica", "B", 20)
	pdf.SetTextColor(0, 0, 0)
	pdf.CellFormat(pdfUsableWidth, 10, "FINANCIAL STATEMENT", "", 1, "L", false, 0, "")

	pdf.SetFont("helvetica", "", 11)
	pdf.SetTextColor(90, 90, 90)
	pdf.CellFormat(pdfUsableWidth, 7, dateRangeLabel, "", 1, "L", false, 0, "")

	pdf.Ln(2)
	pdf.SetDrawColor(0, 0, 0)
	pdf.SetLineWidth(0.6)
	y := pdf.GetY()
	pdf.Line(pdfMarginMM, y, pdfMarginMM+pdfUsableWidth, y)
	pdf.Ln(8)
}

// drawPDFSummaryBoxes draws the three bordered headline figures (Total
// Income, Total Expense, Net Savings) side by side, each a thin black
// rule box with a small gray caps label and a large bold monospaced
// figure — the app's Sans/Mono pairing rendered in gofpdf's core
// Helvetica/Courier fonts, which need no embedded font files.
func drawPDFSummaryBoxes(pdf *gofpdf.Fpdf, summary exportSummary) {
	const gap = 5.0
	const boxHeight = 24.0
	boxWidth := (pdfUsableWidth - 2*gap) / 3
	startX := pdfMarginMM
	y := pdf.GetY()

	boxes := []struct {
		Label string
		Value string
	}{
		{"TOTAL INCOME", formatCentsGrouped(summary.TotalIncomeCents)},
		{"TOTAL EXPENSE", formatCentsGrouped(summary.TotalExpenseCents)},
		{"NET SAVINGS", formatCentsGrouped(summary.NetSavingsCents)},
	}

	pdf.SetDrawColor(0, 0, 0)
	pdf.SetLineWidth(0.3)

	for i, box := range boxes {
		x := startX + float64(i)*(boxWidth+gap)
		pdf.Rect(x, y, boxWidth, boxHeight, "D")

		pdf.SetXY(x+4, y+4)
		pdf.SetFont("helvetica", "", 8)
		pdf.SetTextColor(110, 110, 110)
		pdf.CellFormat(boxWidth-8, 5, box.Label, "", 0, "L", false, 0, "")

		pdf.SetXY(x+4, y+12)
		pdf.SetFont("courier", "B", 15)
		pdf.SetTextColor(0, 0, 0)
		pdf.CellFormat(boxWidth-8, 8, box.Value, "", 0, "L", false, 0, "")
	}

	pdf.SetXY(startX, y+boxHeight+8)
}

// drawPDFTableHeader draws the ledger table's bordered, filled column
// header row at the current position — called once for the first page
// and again at the top of every subsequent page, since a manually
// paginated table (see writePDFExport's row loop) has no other way to
// keep column labels visible throughout a multi-page statement.
func drawPDFTableHeader(pdf *gofpdf.Fpdf) {
	pdf.SetFont("helvetica", "B", 9)
	pdf.SetFillColor(232, 232, 232)
	pdf.SetTextColor(0, 0, 0)
	pdf.SetDrawColor(0, 0, 0)
	pdf.SetLineWidth(0.2)

	for _, col := range pdfColumns {
		pdf.CellFormat(col.Width, pdfHeaderRowHeight, col.Label, "1", 0, col.Align, true, 0, "")
	}
	pdf.Ln(pdfHeaderRowHeight)
}

// drawPDFRow draws one ledger line as a bordered row, zebra-shaded on
// alternating rows for readability. Amount and Fee render in Courier
// (mono) while every other column stays in Helvetica (sans) — the same
// Sans/Mono pairing the frontend uses for figures versus prose. Any
// field too wide for its column is truncated with an ellipsis (see
// truncateToWidth) rather than allowed to overflow into the next cell.
func drawPDFRow(pdf *gofpdf.Fpdf, tr func(string) string, row exportRow, shaded bool) {
	pdf.SetDrawColor(210, 210, 210)
	pdf.SetLineWidth(0.15)
	pdf.SetTextColor(20, 20, 20)
	if shaded {
		pdf.SetFillColor(248, 248, 248)
	} else {
		pdf.SetFillColor(255, 255, 255)
	}

	description := row.Description
	if description == "" {
		description = "—"
	}

	pdf.SetFont("helvetica", "", 8)
	texts := []string{
		row.Date.Format("Jan 02, 2006"),
		truncateToWidth(pdf, tr, description, pdfColumns[1].Width-3),
		truncateToWidth(pdf, tr, row.TypeLabel, pdfColumns[2].Width-3),
		truncateToWidth(pdf, tr, row.CategoryName, pdfColumns[3].Width-3),
		truncateToWidth(pdf, tr, row.WalletLabel, pdfColumns[4].Width-3),
	}
	for i, text := range texts {
		pdf.SetFont("helvetica", "", 8)
		pdf.CellFormat(pdfColumns[i].Width, pdfDataRowHeight, text, "1", 0, pdfColumns[i].Align, shaded, 0, "")
	}

	pdf.SetFont("courier", "", 8)
	pdf.CellFormat(pdfColumns[5].Width, pdfDataRowHeight, formatCents(row.AmountCents), "1", 0, pdfColumns[5].Align, shaded, 0, "")
	pdf.CellFormat(pdfColumns[6].Width, pdfDataRowHeight, formatCents(row.FeeCents), "1", 0, pdfColumns[6].Align, shaded, 0, "")

	pdf.Ln(pdfDataRowHeight)
}

// drawPDFFooter draws the small gray footer every page gets: a fixed
// caption on the left, "Page X of Y" on the right. Registered once via
// SetFooterFunc in writePDFExport and invoked automatically by gofpdf
// as each page is finalized.
func drawPDFFooter(pdf *gofpdf.Fpdf, tr func(string) string) {
	pdf.SetY(-15)
	pdf.SetFont("helvetica", "I", 8)
	pdf.SetTextColor(130, 130, 130)
	half := pdfUsableWidth / 2
	pdf.CellFormat(half, 10, tr("Expense Tracker — Financial Statement"), "", 0, "L", false, 0, "")
	pdf.CellFormat(half, 10, fmt.Sprintf("Page %d of {nb}", pdf.PageNo()), "", 0, "R", false, 0, "")
}

// truncateToWidth shortens s (a plain UTF-8 string, e.g. straight from
// the database — NOT yet run through tr) with a trailing ellipsis until
// its tr-translated form fits within maxWidth mm at pdf's currently
// selected font, measured via GetStringWidth rather than a rough
// character count — the columns use several different font sizes/
// styles, so an estimate would either waste space or silently overflow
// depending on which column called it. The returned string is already
// translated and ready to hand straight to CellFormat.
//
// Truncation walks runes of the original UTF-8 string, not of the
// translated one: tr's output is a single-byte cp1252 encoding, and
// re-slicing that as if it were UTF-8 (via []rune) would misinterpret
// perfectly valid translated bytes as broken UTF-8 and mangle them into
// U+FFFD replacement characters — exactly the mojibake this ordering
// avoids.
func truncateToWidth(pdf *gofpdf.Fpdf, tr func(string) string, s string, maxWidth float64) string {
	translated := tr(s)
	if pdf.GetStringWidth(translated) <= maxWidth {
		return translated
	}
	const ellipsis = "..."
	ellipsisWidth := pdf.GetStringWidth(ellipsis)
	runes := []rune(s)
	for len(runes) > 0 {
		runes = runes[:len(runes)-1]
		candidate := tr(string(runes))
		if pdf.GetStringWidth(candidate)+ellipsisWidth <= maxWidth {
			return candidate + ellipsis
		}
	}
	return ellipsis
}
