package debt

import (
	"time"

	"github.com/milan/expense-tracker/backend/internal/user"
)

// Supported debt directions and lifecycle statuses. Kept as string
// constants (rather than an integer enum) for the same reason as
// account.Type and transaction.Type — the value round-trips through
// JSON in a human-readable, self-describing form.
const (
	TypeLent     = "lent"
	TypeBorrowed = "borrowed"

	StatusPending = "pending"
	StatusSettled = "settled"
)

// Debt is a single money-lent-or-borrowed record between the caller and
// another person (who is not necessarily a user of this app — PersonName
// is a free-text label, not a foreign key). One model covers both
// directions:
//
//   - Type == TypeLent: the caller gave money to PersonName. This is a
//     receivable/asset — RemainingAmount is money owed TO the caller.
//   - Type == TypeBorrowed: the caller received money from PersonName.
//     This is a payable/liability — RemainingAmount is money the caller
//     owes.
//
// RemainingAmount starts equal to TotalAmount and is decremented by each
// Repayment against this debt (see internal/debt/handler.go's
// CreateRepaymentHandler) until it reaches 0, at which point Status
// flips to StatusSettled.
type Debt struct {
	ID     uint `gorm:"primaryKey" json:"id"`
	UserID uint `gorm:"not null;index" json:"user_id"`
	// User is a belongs-to association purely so GORM's AutoMigrate emits
	// a real foreign key constraint on UserID; the handler never
	// populates or serializes it.
	User user.User `gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE" json:"-"`

	PersonName string `gorm:"type:varchar(100);not null" json:"person_name"`
	Type       string `gorm:"type:varchar(20);not null;index" json:"type"`

	// TotalAmount and RemainingAmount are stored in cents (the smallest
	// currency unit), never as floats — see account.Account.Balance for
	// the same rationale.
	TotalAmount     int64 `gorm:"not null" json:"total_amount"`
	RemainingAmount int64 `gorm:"not null" json:"remaining_amount"`

	Status  string    `gorm:"type:varchar(20);not null;index" json:"status"`
	DueDate time.Time `gorm:"not null" json:"due_date"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName pins the table name explicitly so it does not silently
// change if GORM's pluralization rules change across versions.
func (Debt) TableName() string {
	return "debts"
}

// Repayment is a single partial or full payment applied against a Debt.
// Kept as its own table (rather than only mutating Debt.RemainingAmount)
// so a debt's payment history can be listed and audited — mirroring why
// internal/transaction keeps a full ledger instead of just an account
// balance.
type Repayment struct {
	ID     uint `gorm:"primaryKey" json:"id"`
	DebtID uint `gorm:"not null;index" json:"debt_id"`
	Debt   Debt `gorm:"foreignKey:DebtID;constraint:OnDelete:CASCADE" json:"-"`

	// AccountID records which wallet the repayment moved money through
	// (received into, for a lent debt; paid out of, for a borrowed
	// debt). RESTRICT rather than SET NULL: a wallet with repayment
	// history against it should not be deletable out from under that
	// history — wallets in this app are archived (IsActive=false), never
	// hard-deleted, so this constraint is a backstop, not a common path.
	AccountID uint `gorm:"not null;index" json:"account_id"`

	Amount int64     `gorm:"not null" json:"amount"`
	Date   time.Time `gorm:"not null" json:"date"`

	CreatedAt time.Time `json:"created_at"`
}

// TableName pins the table name explicitly so it does not silently
// change if GORM's pluralization rules change across versions.
func (Repayment) TableName() string {
	return "debt_repayments"
}
