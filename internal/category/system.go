package category

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

// The fixed set of system-defined category names a seeded database
// always carries exactly one of each of (see pkg/database/seed.go).
// internal/transaction, internal/debt, and internal/store auto-assign
// these to their own system-generated ledger entries — a wallet
// transfer, a store tab settlement, or a debt/repayment movement — so
// those rows are never left "Uncategorized" the way a real user-entered
// expense/income might legitimately be.
const (
	SystemTransfer        = "Transfer"
	SystemStoreTabPayment = "Store Tab Payment"
	SystemDebtIssued      = "Debt Issued"
	SystemDebtBorrowed    = "Debt Borrowed"
	SystemRepayment       = "Repayment"
)

// ErrSystemCategoryMissing means a lookup for one of the well-known
// system category names above found no row. This can only happen if
// the startup seed (pkg/database.SeedCategories) never ran, or its rows
// were somehow deleted at the database level — a deployment/migration
// problem, not something a client request could have caused — so every
// caller treats it as an internal error (500) rather than a 4xx.
var ErrSystemCategoryMissing = errors.New("system category not found — has the database been seeded?")

// LookupSystemID resolves one of the SystemXxx constants above to its
// category ID. db may be the top-level *gorm.DB or an in-flight
// *gorm.DB transaction — both satisfy this signature identically, the
// same convention ValidateType uses — so callers in internal/transaction,
// internal/debt, and internal/store can call this from inside their own
// db.Transaction blocks to auto-assign a category to a system-generated
// ledger entry the caller never chose one for themselves.
func LookupSystemID(ctx context.Context, db *gorm.DB, name string) (uint, error) {
	var cat Category
	err := db.WithContext(ctx).
		Select("id").
		Where("name = ? AND is_system = ?", name, true).
		First(&cat).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, ErrSystemCategoryMissing
		}
		return 0, err
	}
	return cat.ID, nil
}
