package report

import "time"

// SummaryResponse is the JSON shape returned by GET /reports/summary —
// everything the main dashboard needs in a single round trip.
type SummaryResponse struct {
	// The three headline cards. Each covers the current calendar month
	// (not lifetime totals) with a month-over-month trend, matching the
	// "+12.4% vs last month" style the dashboard displays.
	TotalIncome  MetricWithTrend `json:"total_income"`
	TotalExpense MetricWithTrend `json:"total_expense"`
	NetBalance   MetricWithTrend `json:"net_balance"`

	// CashFlow always has exactly 7 entries, oldest month first, one per
	// calendar month even if that month has zero transactions — a chart
	// with a silently missing month reads as a data gap, not as
	// "nothing happened."
	CashFlow []CashFlowPoint `json:"cash_flow"`

	// CategoryBreakdown covers the current calendar month's expenses
	// only (the same window as the headline cards), largest first,
	// capped at 6 named slices with any remainder folded into "Other".
	CategoryBreakdown []CategoryBreakdownItem `json:"category_breakdown"`

	// RecentTransactions is capped at 5 — this is a glance widget, not
	// the full ledger (that's GET /transactions).
	RecentTransactions []RecentTransaction `json:"recent_transactions"`
}

// MetricWithTrend is a headline figure (cents) plus how it changed
// versus the same figure last calendar month.
type MetricWithTrend struct {
	AmountCents int64 `json:"amount_cents"`
	// ChangePercent is signed — positive means up versus last month,
	// negative means down. Nil when there's nothing meaningful to
	// compare against: last month was exactly zero (division by zero),
	// or the figure crossed zero (e.g. net balance went from a deficit
	// to a surplus) — a percentage across a sign change doesn't mean
	// what a reader would assume it means, so it's omitted rather than
	// shown as a misleading number. See changePercent in handler.go.
	ChangePercent *float64 `json:"change_percent"`
}

// CashFlowPoint is one bar-group's worth of data for the cash flow
// chart: a short month label (e.g. "Jan") plus that month's income and
// expense totals, both in cents.
type CashFlowPoint struct {
	Month   string `json:"month"`
	Income  int64  `json:"income"`
	Expense int64  `json:"expense"`
}

// CategoryBreakdownItem is one slice of the "spend by category" donut.
type CategoryBreakdownItem struct {
	CategoryName string `json:"category_name"`
	AmountCents  int64  `json:"amount_cents"`
	// Percentage is 0-100, of this month's total expense.
	Percentage float64 `json:"percentage"`
	// Color is a real hex value (e.g. "#f97316"), already resolved from
	// the category's stored color token — see category.ColorHex. The
	// frontend renders this directly as an SVG fill; it never needs its
	// own token-to-color table for this chart.
	Color string `json:"color"`
}

// RecentTransaction is a trimmed-down transaction shape carrying only
// what the dashboard's "Recent Transactions" widget renders, with its
// category preloaded (name/color/icon) rather than just a bare ID — the
// widget has no separate category lookup to do.
type RecentTransaction struct {
	ID          uint                       `json:"id"`
	Type        string                     `json:"type"`
	Amount      int64                      `json:"amount"`
	Description string                     `json:"description"`
	Date        time.Time                  `json:"date"`
	Category    *RecentTransactionCategory `json:"category"`
}

// RecentTransactionCategory is the small slice of a category a recent
// transaction row actually renders — a colored badge with a name.
type RecentTransactionCategory struct {
	Name  string `json:"name"`
	Color string `json:"color"` // hex, e.g. "#f97316" — see category.ColorHex
	Icon  string `json:"icon"`
}
