package database

import (
	"fmt"
	"log/slog"

	"gorm.io/gorm"

	"github.com/milan/expense-tracker/backend/internal/category"
)

// systemCategory is the minimal shape needed to insert one seeded
// category. Kept separate from category.Category so this list reads as
// pure data (name/type/color/icon) without the ID/UserID/timestamp
// noise every persisted row also carries.
type systemCategory struct {
	Name  string
	Type  string
	Color string
	Icon  string
}

// systemCategories is the standard category set every user sees, per
// the product spec. Color and Icon are validated against the exact same
// allowlists internal/category/dto.go enforces for user-created
// categories, so a typo here would fail loudly at startup rather than
// silently seeding a category the frontend can't render.
//
// The first nine are ordinary categories a user files their own
// transactions under. The last five (Transfer, Store Tab Payment, Debt
// Issued, Debt Borrowed, Repayment) are auto-assign-only — see
// internal/category/system.go's SystemXxx constants — never something a
// user picks from an "Add Transaction" category dropdown, only
// something internal/transaction, internal/debt, and internal/store
// attach to their own system-generated ledger entries (a transfer, a
// store tab settlement, a debt or repayment movement) so those rows are
// never left "Uncategorized". Type is nominal for these five: they're
// never checked against a transaction's Type the way a real
// income/expense category is (category.ValidateType is never called
// for them — the handlers that assign them look them up directly by
// name via category.LookupSystemID), so the value here is just a
// reasonable best-fit rather than an enforced constraint.
var systemCategories = []systemCategory{
	{Name: "Salary", Type: category.TypeIncome, Color: "green", Icon: "TrendingUp"},
	{Name: "Investments", Type: category.TypeIncome, Color: "indigo", Icon: "TrendingUp"},
	{Name: "Side Hustle", Type: category.TypeIncome, Color: "teal", Icon: "Coins"},
	{Name: "Food & Dining", Type: category.TypeExpense, Color: "orange", Icon: "Utensils"},
	{Name: "Transportation", Type: category.TypeExpense, Color: "blue", Icon: "Car"},
	{Name: "Rent/Housing", Type: category.TypeExpense, Color: "red", Icon: "Home"},
	{Name: "Entertainment", Type: category.TypeExpense, Color: "pink", Icon: "Clapperboard"},
	{Name: "Utilities", Type: category.TypeExpense, Color: "yellow", Icon: "Zap"},
	{Name: "Medical", Type: category.TypeExpense, Color: "rose", Icon: "HeartPulse"},
	{Name: category.SystemTransfer, Type: category.TypeExpense, Color: "slate", Icon: "ArrowLeftRight"},
	{Name: category.SystemStoreTabPayment, Type: category.TypeExpense, Color: "zinc", Icon: "BookOpen"},
	{Name: category.SystemDebtIssued, Type: category.TypeExpense, Color: "neutral", Icon: "HandCoins"},
	{Name: category.SystemDebtBorrowed, Type: category.TypeIncome, Color: "stone", Icon: "Coins"},
	{Name: category.SystemRepayment, Type: category.TypeIncome, Color: "emerald", Icon: "CheckCircle"},
}

// SeedCategories ensures every category in systemCategories exists
// (UserID nil, IsSystem true — visible to every user, see
// category.Category), inserting only the ones missing by name rather
// than only acting on a completely empty table.
//
// This must stay incremental, not a one-time "table is empty" check:
// systemCategories has grown over this app's lifetime (most recently,
// the five auto-assign-only categories), and an already-running
// installation's categories table is never empty by the time a new
// release adds more rows to seed. If this only ran against an empty
// table, every existing deployment would start its very next
// transfer/debt/repayment failing with a 500 the moment
// category.LookupSystemID can't find a category that was supposed to
// have been seeded — an upsert-missing-by-name approach instead lets
// each release's new system categories backfill themselves in on the
// next startup, while never touching a user's own custom categories
// (which are scoped by UserID, not by this Name-based lookup) or an
// already-seeded row of the same name.
func SeedCategories(db *gorm.DB, logger *slog.Logger) error {
	// Backfill first: IsSystem is a newer column than this table itself
	// — every category that predates it has UserID nil (it was, and
	// always has been, a global/system category — see model.go's doc
	// comment) but IsSystem still at its default false, since AutoMigrate
	// only adds a column, it never populates one. Without this backfill,
	// the by-name lookup below would not recognize "Salary" or "Food &
	// Dining" as already seeded (they'd have IsSystem still false) and
	// would insert a second, duplicate row for each on every existing
	// installation's next startup.
	if err := db.Model(&category.Category{}).
		Where("user_id IS NULL AND is_system = ?", false).
		Update("is_system", true).Error; err != nil {
		return fmt.Errorf("database: failed to backfill is_system on existing categories: %w", err)
	}

	var existingNames []string
	if err := db.Model(&category.Category{}).
		Where("is_system = ?", true).
		Pluck("name", &existingNames).Error; err != nil {
		return fmt.Errorf("database: failed to check seeded categories: %w", err)
	}
	alreadySeeded := make(map[string]bool, len(existingNames))
	for _, name := range existingNames {
		alreadySeeded[name] = true
	}

	var toCreate []category.Category
	for _, c := range systemCategories {
		if alreadySeeded[c.Name] {
			continue
		}
		toCreate = append(toCreate, category.Category{
			UserID:   nil,
			Name:     c.Name,
			Type:     c.Type,
			Color:    c.Color,
			Icon:     c.Icon,
			IsSystem: true,
		})
	}
	if len(toCreate) == 0 {
		return nil
	}

	if err := db.Create(&toCreate).Error; err != nil {
		return fmt.Errorf("database: failed to seed categories: %w", err)
	}

	logger.Info("seeded system categories", "count", len(toCreate))
	return nil
}
