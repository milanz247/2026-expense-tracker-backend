package report

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/milan/expense-tracker/backend/internal/middleware"
	"github.com/milan/expense-tracker/backend/internal/transaction"
)

// exportDateLayout is the query-string date format for start_date/end_date
// — a plain "YYYY-MM-DD", matching what a native <input type="date">
// produces, so the frontend never needs to serialize a full RFC3339
// timestamp just to build a download link.
const exportDateLayout = "2006-01-02"

// validExportTypes is every transaction.Type the export endpoint accepts
// as a ?type= filter — the three public types plus the four internal
// ledger types (debt_lent, debt_borrowed, repayment_received,
// repayment_paid) that never reach the public POST /transactions
// endpoint but do live in the same transactions table (see
// internal/debt and internal/store, which write them as the ledger side
// of every debt/store movement). A statement export is meant to show
// the caller's complete financial picture, not just what
// POST /transactions could have created — that's what makes scanning
// the transactions table alone sufficient to satisfy "combining
// transactions, store purchases, and repayments": store purchases and
// repayments already write a row here, so there is no second table to
// join.
var validExportTypes = map[string]bool{
	transaction.TypeIncome:            true,
	transaction.TypeExpense:           true,
	transaction.TypeTransfer:          true,
	transaction.TypeDebtLent:          true,
	transaction.TypeDebtBorrowed:      true,
	transaction.TypeRepaymentReceived: true,
	transaction.TypeRepaymentPaid:     true,
}

// exportTypeLabels renders each stored Type value as the human-readable
// label the CSV/PDF "Type" column shows.
var exportTypeLabels = map[string]string{
	transaction.TypeIncome:            "Income",
	transaction.TypeExpense:           "Expense",
	transaction.TypeTransfer:          "Transfer",
	transaction.TypeDebtLent:          "Debt Lent",
	transaction.TypeDebtBorrowed:      "Debt Borrowed",
	transaction.TypeRepaymentReceived: "Repayment Received",
	transaction.TypeRepaymentPaid:     "Repayment Paid",
}

// exportRow is one ledger line, already resolved into display-ready
// strings and a signed cents figure — CSV and PDF rendering both
// consume this shape directly rather than re-deriving it from a raw
// transaction.Transaction.
type exportRow struct {
	Date         time.Time
	Description  string
	TypeLabel    string
	CategoryName string
	WalletLabel  string
	// AmountCents is signed: positive for money moving toward the
	// caller (income, borrowing, receiving a repayment), negative for
	// money moving away (expense, lending, paying a repayment). A
	// transfer is neither — it keeps its plain positive magnitude,
	// since it neither grows nor shrinks the caller's total net worth.
	AmountCents int64
	// FeeCents is always a positive magnitude regardless of type.
	FeeCents int64
}

// exportSummary is the export's three headline figures. Computed with
// the exact same rule as internal/report's monthly dashboard summary
// (see handler.go's periodTotals/GetSummaryHandler): expense totals
// fold in every fee in the filtered window, not just fees on expense
// rows, and transfers/debt/repayment ledger types never count as
// income or expense — kept consistent so a figure never means two
// different things depending on which screen shows it.
type exportSummary struct {
	TotalIncomeCents  int64
	TotalExpenseCents int64
	NetSavingsCents   int64
}

// RegisterRoutes for the export endpoint is intentionally added to the
// existing report.Handler in handler.go's RegisterRoutes — see that
// method. This file only holds ExportHandler and its helpers.

// ExportHandler handles GET /reports/export. It supports optional
// start_date, end_date (both "YYYY-MM-DD", inclusive), account_id,
// category_id, and type query filters, and a required format
// ("csv" or "pdf") that selects which renderer produces the response
// body. Every filter is applied at the SQL layer so the response is
// never larger than what actually matches, however far back the
// caller's history goes.
func (h *Handler) ExportHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format != "csv" && format != "pdf" {
		writeError(w, http.StatusBadRequest, "format must be 'csv' or 'pdf'")
		return
	}

	query := h.db.WithContext(r.Context()).
		Preload("Category").
		Preload("Account").
		Preload("SourceAccount").
		Preload("DestinationAccount").
		Where("user_id = ?", userID)

	var startDate, endDate time.Time
	var hasStartDate, hasEndDate bool

	if raw := r.URL.Query().Get("start_date"); raw != "" {
		parsed, err := time.Parse(exportDateLayout, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "start_date must be in YYYY-MM-DD format")
			return
		}
		startDate, hasStartDate = parsed, true
		query = query.Where("date >= ?", parsed)
	}

	if raw := r.URL.Query().Get("end_date"); raw != "" {
		parsed, err := time.Parse(exportDateLayout, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "end_date must be in YYYY-MM-DD format")
			return
		}
		endDate, hasEndDate = parsed, true
		// end_date is inclusive of the whole calendar day, so the
		// upper bound used in the query is the start of the next day.
		query = query.Where("date < ?", parsed.AddDate(0, 0, 1))
	}

	if raw := r.URL.Query().Get("account_id"); raw != "" {
		accountID, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "account_id must be a positive integer")
			return
		}
		// Same three-column OR as ListTransactionsHandler: a wallet can
		// be referenced as the plain account_id (income/expense/debt/
		// repayment) or as either side of a transfer.
		query = query.Where(
			"account_id = ? OR source_account_id = ? OR destination_account_id = ?",
			accountID, accountID, accountID,
		)
	}

	if raw := r.URL.Query().Get("category_id"); raw != "" {
		categoryID, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "category_id must be a positive integer")
			return
		}
		query = query.Where("category_id = ?", categoryID)
	}

	if raw := r.URL.Query().Get("type"); raw != "" {
		if !validExportTypes[raw] {
			writeError(w, http.StatusBadRequest, "invalid type filter")
			return
		}
		query = query.Where("type = ?", raw)
	}

	var transactions []transaction.Transaction
	if err := query.Order("date asc, id asc").Find(&transactions).Error; err != nil {
		h.logger.Error("failed to load transactions for export", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to generate report")
		return
	}

	rows := buildExportRows(transactions)
	summary := summarizeExportRows(transactions)

	switch format {
	case "csv":
		writeCSVExport(w, rows)
	case "pdf":
		dateRange := exportDateRangeLabel(startDate, hasStartDate, endDate, hasEndDate)
		if err := writePDFExport(w, rows, summary, dateRange); err != nil {
			h.logger.Error("failed to generate pdf report", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to generate report")
			return
		}
	}
}

// buildExportRows resolves each raw transaction into a display-ready
// exportRow. Preloaded associations (Category/Account/SourceAccount/
// DestinationAccount) mean this is pure in-memory formatting — no
// further database round trips.
func buildExportRows(transactions []transaction.Transaction) []exportRow {
	rows := make([]exportRow, len(transactions))
	for i, t := range transactions {
		rows[i] = exportRow{
			Date:         t.Date,
			Description:  t.Description,
			TypeLabel:    exportTypeLabels[t.Type],
			CategoryName: exportCategoryLabel(t),
			WalletLabel:  exportWalletLabel(t),
			AmountCents:  exportSignedAmount(t),
			FeeCents:     t.Fee,
		}
	}
	return rows
}

// exportCategoryLabel renders a transaction's category for display: the
// category's own name when one is attached, "Uncategorized" for an
// income/expense entry that simply has none, and an em dash for every
// other type (transfer, debt, repayment) — none of which have a
// category concept at all, so "Uncategorized" would misleadingly imply
// one was expected.
func exportCategoryLabel(t transaction.Transaction) string {
	if t.Category != nil {
		return t.Category.Name
	}
	if t.Type == transaction.TypeIncome || t.Type == transaction.TypeExpense {
		return "Uncategorized"
	}
	return "—"
}

// exportWalletLabel renders which wallet(s) a transaction touched: a
// "Source → Destination" pair for a transfer, or the single account's
// name for every other type (income, expense, debt, repayment all set
// AccountID, never Source/DestinationAccountID — see
// transaction.Transaction's doc comment).
func exportWalletLabel(t transaction.Transaction) string {
	if t.Type == transaction.TypeTransfer {
		source := "Unknown"
		if t.SourceAccount != nil {
			source = t.SourceAccount.Name
		}
		destination := "Unknown"
		if t.DestinationAccount != nil {
			destination = t.DestinationAccount.Name
		}
		// A plain ASCII arrow, not "→": this label is shared verbatim
		// with the PDF renderer, whose core fonts only support the
		// cp1252 code page (see export_pdf.go's UnicodeTranslatorFromDescriptor) —
		// U+2192 has no cp1252 mapping and would render as a dropped
		// glyph there.
		return source + " -> " + destination
	}
	if t.Account != nil {
		return t.Account.Name
	}
	return "—"
}

// exportSignedAmount returns a transaction's amount signed by the
// direction money actually moved for the caller: positive for an
// inflow (income, borrowing money, receiving a repayment), negative for
// an outflow (expense, lending money, paying a repayment). A transfer
// keeps its plain unsigned magnitude — it moves money between the
// caller's own wallets, neither growing nor shrinking their total.
func exportSignedAmount(t transaction.Transaction) int64 {
	switch t.Type {
	case transaction.TypeIncome, transaction.TypeDebtBorrowed, transaction.TypeRepaymentReceived:
		return t.Amount
	case transaction.TypeExpense, transaction.TypeDebtLent, transaction.TypeRepaymentPaid:
		return -t.Amount
	default: // transfer
		return t.Amount
	}
}

// summarizeExportRows computes the three headline figures for the
// filtered window, mirroring periodTotals in handler.go exactly: total
// expense folds in every fee across the whole filtered set (not only
// fees on expense-typed rows), and transfer/debt/repayment ledger
// types never contribute to either total — they are wallet-to-wallet or
// wallet-to-person movements, not earned or spent money.
func summarizeExportRows(transactions []transaction.Transaction) exportSummary {
	var income, expense, fees int64
	for _, t := range transactions {
		switch t.Type {
		case transaction.TypeIncome:
			income += t.Amount
		case transaction.TypeExpense:
			expense += t.Amount
		}
		fees += t.Fee
	}
	totalExpense := expense + fees
	return exportSummary{
		TotalIncomeCents:  income,
		TotalExpenseCents: totalExpense,
		NetSavingsCents:   income - totalExpense,
	}
}

// exportDateRangeLabel renders the filter window for the PDF's header
// subtitle, e.g. "Jan 01, 2026 – Jul 18, 2026", "From Jan 01, 2026", "Through Jul 18, 2026",
// or "All Time" when neither bound was supplied.
func exportDateRangeLabel(start time.Time, hasStart bool, end time.Time, hasEnd bool) string {
	const layout = "Jan 02, 2006"
	switch {
	case hasStart && hasEnd:
		return start.Format(layout) + " – " + end.Format(layout)
	case hasStart:
		return "From " + start.Format(layout)
	case hasEnd:
		return "Through " + end.Format(layout)
	default:
		return "All Time"
	}
}

// formatCents renders a signed integer cents value as a plain decimal
// major-unit string (e.g. -4550 -> "-45.50", 0 -> "0.00") with no
// thousands separators and no currency symbol — safe for a spreadsheet
// column to parse as a number, and reused by the PDF table for the same
// per-row figures.
func formatCents(cents int64) string {
	sign := ""
	if cents < 0 {
		sign = "-"
		cents = -cents
	}
	return sign + strconv.FormatInt(cents/100, 10) + "." + twoDigits(cents%100)
}

func twoDigits(n int64) string {
	if n < 10 {
		return "0" + strconv.FormatInt(n, 10)
	}
	return strconv.FormatInt(n, 10)
}

// formatCentsGrouped is formatCents with thousands separators inserted
// into the integer part — used only for the PDF summary boxes' large
// headline figures, where the extra readability is worth the extra
// formatting cost; the CSV and table rows stay in plain formatCents
// form since they're meant to be machine/spreadsheet friendly.
func formatCentsGrouped(cents int64) string {
	plain := formatCents(cents)
	sign := ""
	if strings.HasPrefix(plain, "-") {
		sign = "-"
		plain = plain[1:]
	}
	parts := strings.SplitN(plain, ".", 2)
	intPart := parts[0]

	var grouped strings.Builder
	for i, digit := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			grouped.WriteByte(',')
		}
		grouped.WriteRune(digit)
	}

	return sign + grouped.String() + "." + parts[1]
}
