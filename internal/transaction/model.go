package transaction

import (
	"time"

	"github.com/milan/expense-tracker/backend/internal/account"
	"github.com/milan/expense-tracker/backend/internal/category"
	"github.com/milan/expense-tracker/backend/internal/user"
)

// Transaction is a single ledger entry: an income credit, an expense
// debit, or a transfer between two of the caller's own wallets.
//
// Exactly one of the account fields is populated depending on Type:
//   - income / expense: AccountID is set, SourceAccountID and
//     DestinationAccountID are nil.
//   - transfer: SourceAccountID and DestinationAccountID are set,
//     AccountID is nil.
//
// See internal/transaction/handler.go's CreateTransactionHandler for the
// balance arithmetic and the GORM transaction that makes each of these
// atomic.
type Transaction struct {
	ID     uint `gorm:"primaryKey" json:"id"`
	UserID uint `gorm:"not null;index" json:"user_id"`
	// User is a belongs-to association purely so GORM's AutoMigrate
	// emits a real foreign key constraint on UserID (see account.Account
	// for the same pattern); the handler never populates or serializes it.
	User user.User `gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE" json:"-"`

	Type string `gorm:"type:varchar(20);not null;index" json:"type"`
	// Amount and Fee are stored in cents (the smallest currency unit),
	// never as floats — see account.Account.Balance for the same
	// rationale.
	Amount int64 `gorm:"not null" json:"amount"`
	Fee    int64 `gorm:"not null;default:0" json:"fee"`

	AccountID *uint            `gorm:"index" json:"account_id"`
	Account   *account.Account `gorm:"foreignKey:AccountID;constraint:OnDelete:SET NULL" json:"-"`

	SourceAccountID *uint            `gorm:"index" json:"source_account_id"`
	SourceAccount   *account.Account `gorm:"foreignKey:SourceAccountID;constraint:OnDelete:SET NULL" json:"-"`

	DestinationAccountID *uint            `gorm:"index" json:"destination_account_id"`
	DestinationAccount   *account.Account `gorm:"foreignKey:DestinationAccountID;constraint:OnDelete:SET NULL" json:"-"`

	// CategoryID references internal/category.Category. The constraint is
	// RESTRICT rather than SET NULL (unlike the account associations
	// above): internal/category's DeleteCategoryHandler already checks
	// for referencing transactions and rejects the delete with a clear
	// 409 before this constraint would ever fire, so RESTRICT here is
	// purely a last-line-of-defense backstop against orphaned category
	// references, matching the product rule that a category in use can
	// never be deleted out from under existing ledger entries.
	CategoryID *uint              `gorm:"index" json:"category_id"`
	Category   *category.Category `gorm:"foreignKey:CategoryID;constraint:OnDelete:RESTRICT" json:"-"`

	Description string    `gorm:"type:varchar(255)" json:"description"`
	Date        time.Time `gorm:"not null;index" json:"date"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// TableName pins the table name explicitly so it does not silently
// change if GORM's pluralization rules change across versions.
func (Transaction) TableName() string {
	return "transactions"
}
