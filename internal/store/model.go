package store

import (
	"time"

	"github.com/milan/expense-tracker/backend/internal/account"
	"github.com/milan/expense-tracker/backend/internal/category"
	"github.com/milan/expense-tracker/backend/internal/user"
)

// Creditor is a persistent local shop the caller runs a running credit
// tab with (a "store tab" / කඩේ පොත්). Unlike internal/debt.Debt, this
// row is never expected to be deleted once OutstandingDebt reaches
// zero — a shop the caller regularly buys from stays in the list,
// billing-settled, ready for the next purchase. See
// handler.go's RecordStoreSettlementHandler for why IsActive stays true
// even at zero debt.
type Creditor struct {
	ID     uint `gorm:"primaryKey" json:"id"`
	UserID uint `gorm:"not null;index" json:"user_id"`
	// User is a belongs-to association purely so GORM's AutoMigrate emits
	// a real foreign key constraint on UserID; the handler never
	// populates or serializes it.
	User user.User `gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE" json:"-"`

	Name string `gorm:"type:varchar(100);not null" json:"name"`
	// OutstandingDebt is stored in cents (the smallest currency unit),
	// never as a float — see account.Account.Balance for the same
	// rationale. It only ever increases via Purchase and decreases via
	// Settlement, both inside their own db.Transaction (see handler.go).
	OutstandingDebt int64 `gorm:"not null;default:0" json:"outstanding_debt"`
	// IsActive marks whether this shop still appears in the "add a
	// purchase against" picker. A shop is never hard-deleted — its
	// purchase/settlement history must remain queryable — so this is the
	// only lifecycle flag it has, and it defaults true and is never
	// flipped false by reaching a zero balance (see RecordStoreSettlementHandler).
	IsActive bool `gorm:"not null;default:true;index" json:"is_active"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName pins the table name explicitly so it does not silently
// change if GORM's pluralization rules change across versions.
func (Creditor) TableName() string {
	return "store_creditors"
}

// Purchase is a single credit purchase against a Creditor's tab —
// buying something now, to be paid for later. Recording one increases
// the shop's OutstandingDebt and simultaneously creates a global expense
// Transaction under the selected category (see
// handler.go's RecordStorePurchaseHandler), so it shows up in the
// user's monthly expense reports immediately rather than only when the
// tab is eventually settled.
type Purchase struct {
	ID         uint     `gorm:"primaryKey" json:"id"`
	CreditorID uint     `gorm:"not null;index" json:"creditor_id"`
	Creditor   Creditor `gorm:"foreignKey:CreditorID;constraint:OnDelete:CASCADE" json:"-"`

	// Amount and Fee are stored in cents, same convention as
	// transaction.Transaction.Amount/Fee.
	Amount int64 `gorm:"not null" json:"amount"`
	Fee    int64 `gorm:"not null;default:0" json:"fee"`

	CategoryID uint              `gorm:"not null;index" json:"category_id"`
	Category   category.Category `gorm:"foreignKey:CategoryID;constraint:OnDelete:RESTRICT" json:"-"`

	// TransactionID links back to the expense ledger row this purchase
	// auto-created, so the two stay traceable to each other. RESTRICT:
	// that transaction should never be deletable out from under this
	// purchase record (transactions in this app are never deleted by any
	// existing endpoint, so this is a backstop, not a live path).
	TransactionID uint `gorm:"not null;index" json:"transaction_id"`

	Description string    `gorm:"type:varchar(255)" json:"description"`
	Date        time.Time `gorm:"not null;index" json:"date"`
	CreatedAt   time.Time `json:"created_at"`
}

// TableName pins the table name explicitly so it does not silently
// change if GORM's pluralization rules change across versions.
func (Purchase) TableName() string {
	return "store_purchases"
}

// categoryOrNil returns a pointer to the preloaded Category, or nil if
// the handler that loaded this Purchase never called .Preload("Category")
// — Category is a value-type association, so its zero value (ID == 0)
// is how GORM leaves it un-preloaded, which would otherwise serialize as
// a misleading empty-but-present category object.
func (p Purchase) categoryOrNil() *category.Category {
	if p.Category.ID == 0 {
		return nil
	}
	c := p.Category
	return &c
}

// Settlement is a single payment applied against a Creditor's
// OutstandingDebt. See handler.go's RecordStoreSettlementHandler for the
// atomic wallet-debit / debt-reduction / optional-fee-expense logic.
type Settlement struct {
	ID         uint     `gorm:"primaryKey" json:"id"`
	CreditorID uint     `gorm:"not null;index" json:"creditor_id"`
	Creditor   Creditor `gorm:"foreignKey:CreditorID;constraint:OnDelete:CASCADE" json:"-"`

	// AccountID records which wallet the payment was made from. RESTRICT
	// rather than SET NULL: a wallet with settlement history against it
	// should not be deletable out from under that history — wallets in
	// this app are archived (IsActive=false), never hard-deleted, so this
	// constraint is a backstop, not a common path.
	AccountID uint            `gorm:"not null;index" json:"account_id"`
	Account   account.Account `gorm:"foreignKey:AccountID;constraint:OnDelete:RESTRICT" json:"-"`

	AmountPaid int64 `gorm:"not null" json:"amount_paid"`
	// Fee, if present, is deducted from the wallet in addition to
	// AmountPaid and logged as its own separate expense transaction (see
	// handler.go) — it is never added into the shop's OutstandingDebt
	// reduction, since it is money paid to a third party (e.g. a bank
	// transfer fee), not to the shop itself.
	Fee int64 `gorm:"not null;default:0" json:"fee"`

	Date      time.Time `gorm:"not null;index" json:"date"`
	CreatedAt time.Time `json:"created_at"`
}

// TableName pins the table name explicitly so it does not silently
// change if GORM's pluralization rules change across versions.
func (Settlement) TableName() string {
	return "store_settlements"
}

// accountOrNil returns a pointer to the preloaded Account, or nil if the
// handler that loaded this Settlement never called .Preload("Account")
// — same value-type-association reasoning as Purchase.categoryOrNil.
func (s Settlement) accountOrNil() *account.Account {
	if s.Account.ID == 0 {
		return nil
	}
	a := s.Account
	return &a
}
