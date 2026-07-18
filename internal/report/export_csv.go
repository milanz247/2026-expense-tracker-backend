package report

import (
	"bytes"
	"encoding/csv"
	"net/http"
	"strconv"
)

// csvExportHeader is the fixed column order for the CSV export, per the
// product spec.
var csvExportHeader = []string{"Date", "Description", "Type", "Category", "Wallet", "Amount", "Fee"}

// writeCSVExport renders rows as a spreadsheet-ready CSV and writes it
// to w as an attachment download. Built into an in-memory buffer first
// (rather than streaming csv.Writer straight at w) so a Content-Length
// header can be set and so nothing is ever written to the response
// before we know the whole document encoded successfully — a partial,
// truncated CSV would be worse than a clean 500.
func writeCSVExport(w http.ResponseWriter, rows []exportRow) {
	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)

	// A write error here can only be a bytes.Buffer allocation failure
	// (out of memory) — encoding/csv never rejects well-formed string
	// records — so there is nothing meaningful to recover from; if it
	// happens, csvWriter.Error() below reports it and we fail closed.
	_ = writer.Write(csvExportHeader)
	for _, row := range rows {
		_ = writer.Write([]string{
			row.Date.Format(exportDateLayout),
			row.Description,
			row.TypeLabel,
			row.CategoryName,
			row.WalletLabel,
			formatCents(row.AmountCents),
			formatCents(row.FeeCents),
		})
	}
	writer.Flush()

	if err := writer.Error(); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"failed to generate report"}`))
		return
	}

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="financial_report.csv"`)
	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}
