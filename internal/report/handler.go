package report

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"time"

	"gorm.io/gorm"

	"github.com/milan/expense-tracker/backend/internal/category"
	"github.com/milan/expense-tracker/backend/internal/middleware"
	"github.com/milan/expense-tracker/backend/internal/transaction"
)

// cashFlowMonths is how many months (including the current one) the
// cash flow chart covers.
const cashFlowMonths = 7

// maxCategorySlices is how many named slices the category breakdown
// shows before folding the remainder into a single "Other" bucket — a
// donut with more wedges than this stops being readable at a glance,
// and (per this app's dataviz conventions) a categorical chart with
// more than a handful of series should fold the tail rather than keep
// generating distinct colors for it.
const maxCategorySlices = 6

// otherCategoryColor is zinc-500 — a neutral that reads as "everything
// else" rather than impersonating a real category's color.
const otherCategoryColor = "#71717a"

// Handler exposes the reporting/analytics endpoints that power the main
// dashboard. Read-only by design — it never writes to the database, only
// aggregates what internal/transaction has already written.
type Handler struct {
	db     *gorm.DB
	logger *slog.Logger
}

// NewHandler wires a *gorm.DB and *slog.Logger into a new report Handler.
func NewHandler(db *gorm.DB, logger *slog.Logger) *Handler {
	return &Handler{db: db, logger: logger}
}

// RegisterRoutes attaches the handler's endpoints to the given mux under
// the supplied prefix (e.g. "/api/v1").
func (h *Handler) RegisterRoutes(mux *http.ServeMux, prefix string, requireAuth func(http.Handler) http.Handler) {
	mux.Handle("GET "+prefix+"/reports/summary", requireAuth(http.HandlerFunc(h.GetSummaryHandler)))
	mux.Handle("GET "+prefix+"/reports/export", requireAuth(http.HandlerFunc(h.ExportHandler)))
}

// GetSummaryHandler handles GET /reports/summary. Every figure is
// computed with SQL-side aggregation (SUM/CASE, GROUP BY, JOIN) rather
// than loading a user's full transaction history into Go and summing it
// in application code — four indexed queries total, regardless of how
// many transactions the account has accumulated.
func (h *Handler) GetSummaryHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	ctx := r.Context()
	now := time.Now().UTC()
	thisMonthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	nextMonthStart := thisMonthStart.AddDate(0, 1, 0)
	lastMonthStart := thisMonthStart.AddDate(0, -1, 0)

	// 1) This month vs last month totals — one query, one pass over a
	// narrow two-month slice of the table.
	periods, err := h.periodTotals(ctx, userID, lastMonthStart, thisMonthStart, nextMonthStart)
	if err != nil {
		h.logger.Error("failed to aggregate period totals", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load summary")
		return
	}

	thisExpense := periods.ThisExpense + periods.ThisFees
	lastExpense := periods.LastExpense + periods.LastFees
	thisNet := periods.ThisIncome - thisExpense
	lastNet := periods.LastIncome - lastExpense

	// 2) Cash flow: one grouped query for the whole 7-month window, not
	// seven separate per-month queries.
	cashFlow, err := h.cashFlowData(ctx, userID, now)
	if err != nil {
		h.logger.Error("failed to aggregate cash flow", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load summary")
		return
	}

	// 3) Category breakdown for the current month only.
	categoryBreakdown, err := h.categoryBreakdown(ctx, userID, thisMonthStart, nextMonthStart)
	if err != nil {
		h.logger.Error("failed to aggregate category breakdown", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load summary")
		return
	}

	// 4) Recent activity, category preloaded so the widget needs no
	// separate lookup.
	var recent []transaction.Transaction
	if err := h.db.WithContext(ctx).
		Preload("Category").
		Where("user_id = ?", userID).
		Order("date desc, id desc").
		Limit(5).
		Find(&recent).Error; err != nil {
		h.logger.Error("failed to load recent transactions", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load summary")
		return
	}

	recentResp := make([]RecentTransaction, len(recent))
	for i, t := range recent {
		item := RecentTransaction{
			ID:          t.ID,
			Type:        t.Type,
			Amount:      t.Amount,
			Description: t.Description,
			Date:        t.Date,
		}
		if t.Category != nil {
			item.Category = &RecentTransactionCategory{
				Name:  t.Category.Name,
				Color: category.ColorHex(t.Category.Color),
				Icon:  t.Category.Icon,
			}
		}
		recentResp[i] = item
	}

	writeJSON(w, http.StatusOK, SummaryResponse{
		TotalIncome: MetricWithTrend{
			AmountCents:   periods.ThisIncome,
			ChangePercent: changePercent(periods.ThisIncome, periods.LastIncome),
		},
		TotalExpense: MetricWithTrend{
			AmountCents:   thisExpense,
			ChangePercent: changePercent(thisExpense, lastExpense),
		},
		NetBalance: MetricWithTrend{
			AmountCents:   thisNet,
			ChangePercent: changePercent(thisNet, lastNet),
		},
		CashFlow:           cashFlow,
		CategoryBreakdown:  categoryBreakdown,
		RecentTransactions: recentResp,
	})
}

// periodTotalsRow is the scan target for periodTotals' single query.
type periodTotalsRow struct {
	ThisIncome  int64
	ThisExpense int64
	ThisFees    int64
	LastIncome  int64
	LastExpense int64
	LastFees    int64
}

// periodTotals sums income, expense, and fees for the current calendar
// month and the one before it in a single query: the WHERE clause
// restricts to the two-month window [lastMonthStart, nextMonthStart),
// and each SELECT expression buckets a row into "this month" or "last
// month" by comparing its date against thisMonthStart — one table scan
// instead of two separate month-by-month queries.
func (h *Handler) periodTotals(ctx context.Context, userID uint, lastMonthStart, thisMonthStart, nextMonthStart time.Time) (periodTotalsRow, error) {
	var row periodTotalsRow
	err := h.db.WithContext(ctx).
		Model(&transaction.Transaction{}).
		Where("user_id = ? AND date >= ? AND date < ?", userID, lastMonthStart, nextMonthStart).
		Select(
			"COALESCE(SUM(CASE WHEN date >= ? AND type = ? THEN amount ELSE 0 END), 0) AS this_income, "+
				"COALESCE(SUM(CASE WHEN date >= ? AND type = ? THEN amount ELSE 0 END), 0) AS this_expense, "+
				"COALESCE(SUM(CASE WHEN date >= ? THEN fee ELSE 0 END), 0) AS this_fees, "+
				"COALESCE(SUM(CASE WHEN date < ? AND type = ? THEN amount ELSE 0 END), 0) AS last_income, "+
				"COALESCE(SUM(CASE WHEN date < ? AND type = ? THEN amount ELSE 0 END), 0) AS last_expense, "+
				"COALESCE(SUM(CASE WHEN date < ? THEN fee ELSE 0 END), 0) AS last_fees",
			thisMonthStart, transaction.TypeIncome,
			thisMonthStart, transaction.TypeExpense,
			thisMonthStart,
			thisMonthStart, transaction.TypeIncome,
			thisMonthStart, transaction.TypeExpense,
			thisMonthStart,
		).
		Scan(&row).Error
	return row, err
}

// changePercent computes the signed percentage change from previous to
// current, or nil when that percentage wouldn't mean what a reader
// assumes it means: previous being exactly zero (undefined — division
// by zero), or the value crossing zero (e.g. net balance flipping from
// a deficit to a surplus) — "the deficit shrank by 160%" is not a
// meaningful statement, so it's omitted rather than shown.
func changePercent(current, previous int64) *float64 {
	if previous == 0 {
		return nil
	}
	if (previous > 0) != (current > 0) && current != 0 {
		return nil
	}
	pct := (float64(current-previous) / math.Abs(float64(previous))) * 100
	return &pct
}

// cashFlowRow is the scan target for the monthly-aggregation query —
// one row per calendar month that actually has transactions in the
// window (a month with none simply won't appear, and is filled in as
// zero afterward).
type cashFlowRow struct {
	// Period, not "year_month": YEAR_MONTH is a reserved word in MySQL
	// (a valid INTERVAL unit, as in "INTERVAL 1 YEAR_MONTH"), so using
	// it unquoted as a SELECT alias is a syntax error.
	Period  string
	Income  int64
	Expense int64
}

// cashFlowData returns exactly cashFlowMonths CashFlowPoints, oldest
// first, covering the current calendar month and the months before it.
func (h *Handler) cashFlowData(ctx context.Context, userID uint, now time.Time) ([]CashFlowPoint, error) {
	windowStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -(cashFlowMonths - 1), 0)

	var rows []cashFlowRow
	if err := h.db.WithContext(ctx).
		Model(&transaction.Transaction{}).
		Where("user_id = ? AND date >= ?", userID, windowStart).
		Select(
			"DATE_FORMAT(date, '%Y-%m') AS period, "+
				"COALESCE(SUM(CASE WHEN type = ? THEN amount ELSE 0 END), 0) AS income, "+
				"COALESCE(SUM(CASE WHEN type = ? THEN amount ELSE 0 END), 0) AS expense",
			transaction.TypeIncome, transaction.TypeExpense,
		).
		Group("period").
		Scan(&rows).Error; err != nil {
		return nil, err
	}

	byMonth := make(map[string]cashFlowRow, len(rows))
	for _, row := range rows {
		byMonth[row.Period] = row
	}

	points := make([]CashFlowPoint, 0, cashFlowMonths)
	for i := cashFlowMonths - 1; i >= 0; i-- {
		monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -i, 0)
		key := monthStart.Format("2006-01")
		row := byMonth[key] // zero value if this month had none

		points = append(points, CashFlowPoint{
			Month:   monthStart.Format("Jan"),
			Income:  row.Income,
			Expense: row.Expense,
		})
	}

	return points, nil
}

// categoryBreakdownRow is the scan target for categoryBreakdown's query.
type categoryBreakdownRow struct {
	CategoryName string
	ColorToken   string
	Amount       int64
}

// categoryBreakdown sums this month's expenses grouped by category, via
// a LEFT JOIN against categories by raw table name (a custom aggregate
// SELECT with computed aliases has no use for the category.Category
// struct type, just its columns) so an uncategorized expense still
// contributes to the total under an "Uncategorized" bucket rather than
// silently vanishing from the percentages. Sorted largest first, then
// folded to maxCategorySlices named entries plus a single "Other" for
// the remainder.
func (h *Handler) categoryBreakdown(ctx context.Context, userID uint, thisMonthStart, nextMonthStart time.Time) ([]CategoryBreakdownItem, error) {
	var rows []categoryBreakdownRow
	if err := h.db.WithContext(ctx).
		Table("transactions AS t").
		Select("COALESCE(c.name, 'Uncategorized') AS category_name, COALESCE(c.color, '') AS color_token, SUM(t.amount) AS amount").
		Joins("LEFT JOIN categories AS c ON c.id = t.category_id").
		Where("t.user_id = ? AND t.type = ? AND t.date >= ? AND t.date < ?", userID, transaction.TypeExpense, thisMonthStart, nextMonthStart).
		Group("category_name, color_token").
		Order("amount DESC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}

	var total int64
	for _, row := range rows {
		total += row.Amount
	}
	if total == 0 {
		return []CategoryBreakdownItem{}, nil
	}

	named := rows
	var otherAmount int64
	if len(rows) > maxCategorySlices {
		named = rows[:maxCategorySlices]
		for _, row := range rows[maxCategorySlices:] {
			otherAmount += row.Amount
		}
	}

	items := make([]CategoryBreakdownItem, 0, len(named)+1)
	for _, row := range named {
		items = append(items, CategoryBreakdownItem{
			CategoryName: row.CategoryName,
			AmountCents:  row.Amount,
			Percentage:   float64(row.Amount) / float64(total) * 100,
			Color:        category.ColorHex(row.ColorToken),
		})
	}
	if otherAmount > 0 {
		items = append(items, CategoryBreakdownItem{
			CategoryName: "Other",
			AmountCents:  otherAmount,
			Percentage:   float64(otherAmount) / float64(total) * 100,
			Color:        otherCategoryColor,
		})
	}

	// Order is already largest-first from the SQL query for the named
	// slice; re-sorting here is only needed because "Other" was appended
	// after the fact and must still slot in by size, not always last.
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].AmountCents > items[j].AmountCents
	})

	return items, nil
}

// writeJSON marshals payload as JSON and writes it to w with the given
// status code, setting the appropriate Content-Type header.
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("failed to write JSON response", "error", err)
	}
}

// errorResponse is the standard JSON shape returned for any handled error.
type errorResponse struct {
	Error string `json:"error"`
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, errorResponse{Error: message})
}
