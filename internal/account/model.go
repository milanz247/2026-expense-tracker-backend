package account

import (
	"time"

	"github.com/milan/expense-tracker/backend/internal/user"
)

// Account represents a single wallet/financial account owned by a user
// (e.g. "Cash", "Commercial Bank", "Visa Credit Card"). Balance is stored
// as an integer number of cents (the smallest currency unit) rather than
// a float, since floating point arithmetic is not safe for money.
type Account struct {
	ID     uint `gorm:"primaryKey" json:"id"`
	UserID uint `gorm:"not null;index" json:"user_id"`
	// User is a belongs-to association purely so GORM's AutoMigrate emits
	// a real foreign key constraint on UserID; the handler never
	// populates or serializes it.
	User    user.User `gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE" json:"-"`
	Name    string    `gorm:"type:varchar(100);not null" json:"name"`
	Type    string    `gorm:"type:varchar(20);not null" json:"type"`
	Balance int64     `gorm:"not null;default:0" json:"balance"`
	// CreditLimit only applies to Type == TypeCreditCard, in which case
	// Balance is a negative number of cents representing debt (e.g.
	// Balance -15000 = 150.00 owed). The transaction handler enforces
	// Balance >= -CreditLimit for that account type; for every other
	// type CreditLimit stays 0 and Balance must never go negative — see
	// internal/transaction/handler.go's validateOutgoing.
	CreditLimit int64 `gorm:"not null;default:0" json:"credit_limit"`
	// IsActive is the wallet's soft-delete flag. Archiving a wallet
	// (setting this false) hides it from transaction forms without
	// touching its balance or transaction history — real money movements
	// are never deleted, only the ability to add new ones against a
	// closed wallet.
	IsActive bool `gorm:"not null;default:true;index" json:"is_active"`
	// BranchName/AccountNumber/HolderName are optional reference details
	// for non-cash wallets (bank, credit_card, investment) — purely
	// informational, never used in any balance/ledger arithmetic.
	// Pointers so they can be genuinely NULL rather than an empty string
	// when the caller doesn't supply them (matches Account.UserID's
	// convention elsewhere in this package for an optional column).
	BranchName    *string   `gorm:"type:varchar(100)" json:"branch_name"`
	AccountNumber *string   `gorm:"type:varchar(50)" json:"account_number"`
	HolderName    *string   `gorm:"type:varchar(150)" json:"holder_name"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// TableName pins the table name explicitly so it does not silently change
// if GORM's pluralization rules change across versions.
func (Account) TableName() string {
	return "accounts"
}
