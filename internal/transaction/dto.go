package transaction

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/milan/expense-tracker/backend/internal/account"
	"github.com/milan/expense-tracker/backend/internal/category"
)

// Supported transaction types.
const (
	TypeIncome   = "income"
	TypeExpense  = "expense"
	TypeTransfer = "transfer"

	// The four ledger types below are deliberately NOT added to
	// validTransactionTypes: CreateTransactionRequest.Validate rejects
	// them, so the public POST /transactions endpoint can never be used
	// to create one directly. They're only ever constructed by
	// internal/debt and internal/store, inside their own db.Transaction
	// blocks, as the ledger side-effect of a debt/repayment or store
	// purchase/settlement — never counted as income/expense by
	// internal/report's monthly aggregation (which filters strictly on
	// type = 'income'/'expense'), so lending, borrowing, and repaying
	// money never distorts the user's actual income/spending totals.
	TypeDebtLent          = "debt_lent"
	TypeDebtBorrowed      = "debt_borrowed"
	TypeRepaymentReceived = "repayment_received"
	TypeRepaymentPaid     = "repayment_paid"
)

var validTransactionTypes = map[string]bool{
	TypeIncome:   true,
	TypeExpense:  true,
	TypeTransfer: true,
}

// CreateTransactionRequest is the expected JSON payload for POST
// /transactions. Amount and Fee are expressed in the wallet's major
// currency unit (matching CreateAccountRequest.InitialBalance in
// internal/account) and converted to cents via AmountCents/FeeCents.
//
// Date is a time.Time (not a string) so encoding/json parses an RFC3339
// timestamp for us automatically — no hand-rolled date parsing needed.
type CreateTransactionRequest struct {
	Type                 string  `json:"type"`
	Amount               float64 `json:"amount"`
	Fee                  float64 `json:"fee"`
	AccountID            *uint   `json:"account_id"`
	SourceAccountID      *uint   `json:"source_account_id"`
	DestinationAccountID *uint   `json:"destination_account_id"`
	// CategoryID is optional for income/expense — a transaction doesn't
	// have to be categorized — and always ignored for transfer, per the
	// product rule that a transfer moves money between the caller's own
	// wallets and has no income/expense category to assign. Validate
	// enforces the "ignored" half by nulling it out for that type
	// regardless of what the client sends; the handler enforces the
	// other half (that a supplied category's Type actually matches this
	// transaction's Type) once it has looked the category up, since that
	// needs a database round trip Validate deliberately doesn't make.
	CategoryID  *uint     `json:"category_id"`
	Description string    `json:"description"`
	Date        time.Time `json:"date"`
}

// Validate applies shape and cross-field business rules that don't need
// a database round trip (ownership/active-status/balance checks happen
// in the handler, inside the GORM transaction, since they require
// locking real rows — see lockOwnedActiveAccount).
func (r *CreateTransactionRequest) Validate() error {
	r.Type = strings.ToLower(strings.TrimSpace(r.Type))
	r.Description = strings.TrimSpace(r.Description)

	if !validTransactionTypes[r.Type] {
		return fmt.Errorf("type must be one of: %s, %s, %s", TypeIncome, TypeExpense, TypeTransfer)
	}

	if math.IsNaN(r.Amount) || math.IsInf(r.Amount, 0) || r.Amount <= 0 {
		return errors.New("amount must be a positive, finite number")
	}
	if math.IsNaN(r.Fee) || math.IsInf(r.Fee, 0) || r.Fee < 0 {
		return errors.New("fee must be a non-negative, finite number")
	}

	if len(r.Description) > 255 {
		return errors.New("description must be at most 255 characters")
	}

	if r.Date.IsZero() {
		return errors.New("date is required")
	}

	switch r.Type {
	case TypeIncome, TypeExpense:
		if r.AccountID == nil {
			return errors.New("account_id is required")
		}
		if r.SourceAccountID != nil || r.DestinationAccountID != nil {
			return fmt.Errorf("source_account_id and destination_account_id must not be set for %s transactions", r.Type)
		}
	case TypeTransfer:
		if r.AccountID != nil {
			return errors.New("account_id must not be set for transfer transactions")
		}
		if r.SourceAccountID == nil || r.DestinationAccountID == nil {
			return errors.New("source_account_id and destination_account_id are required for transfer transactions")
		}
		if *r.SourceAccountID == *r.DestinationAccountID {
			return errors.New("source_account_id and destination_account_id must be different")
		}
		// The Category field is completely irrelevant to a transfer —
		// silently drop whatever the client sent rather than rejecting
		// the request over it.
		r.CategoryID = nil
	}

	return nil
}

// AmountCents converts the human-entered Amount into an integer number
// of cents, rounding to the nearest cent to avoid floating point drift.
func (r *CreateTransactionRequest) AmountCents() int64 {
	return int64(math.Round(r.Amount * 100))
}

// FeeCents converts the human-entered Fee into an integer number of
// cents, rounding to the nearest cent.
func (r *CreateTransactionRequest) FeeCents() int64 {
	return int64(math.Round(r.Fee * 100))
}

// TransactionResponse is the JSON shape returned to clients for a
// single ledger entry. Amount and Fee stay in cents on the wire, same
// convention as AccountResponse.Balance.
//
// Alongside every *ID field, the response also embeds the associated
// Category/Account row itself (nil unless the handler preloaded it) so
// the frontend can render a name/color/icon directly — e.g.
// transaction.category?.name — instead of doing its own ID-to-name
// lookup against a separately fetched list.
type TransactionResponse struct {
	ID                   uint               `json:"id"`
	Type                 string             `json:"type"`
	Amount               int64              `json:"amount"`
	Fee                  int64              `json:"fee"`
	AccountID            *uint              `json:"account_id"`
	Account              *account.Account   `json:"account"`
	SourceAccountID      *uint              `json:"source_account_id"`
	SourceAccount        *account.Account   `json:"source_account"`
	DestinationAccountID *uint              `json:"destination_account_id"`
	DestinationAccount   *account.Account   `json:"destination_account"`
	CategoryID           *uint              `json:"category_id"`
	Category             *category.Category `json:"category"`
	Description          string             `json:"description"`
	Date                 time.Time          `json:"date"`
	CreatedAt            time.Time          `json:"created_at"`
}

func toResponse(t Transaction) TransactionResponse {
	return TransactionResponse{
		ID:                   t.ID,
		Type:                 t.Type,
		Amount:               t.Amount,
		Fee:                  t.Fee,
		AccountID:            t.AccountID,
		Account:              t.Account,
		SourceAccountID:      t.SourceAccountID,
		SourceAccount:        t.SourceAccount,
		DestinationAccountID: t.DestinationAccountID,
		DestinationAccount:   t.DestinationAccount,
		CategoryID:           t.CategoryID,
		Category:             t.Category,
		Description:          t.Description,
		Date:                 t.Date,
		CreatedAt:            t.CreatedAt,
	}
}
