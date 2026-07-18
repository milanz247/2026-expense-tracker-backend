package store

import (
	"errors"
	"math"
	"strings"
	"time"

	"github.com/milan/expense-tracker/backend/internal/account"
	"github.com/milan/expense-tracker/backend/internal/category"
)

// CreateStoreRequest is the expected JSON payload for POST
// /store-creditors.
type CreateStoreRequest struct {
	Name string `json:"name"`
}

// Validate normalizes and applies shape rules.
func (r *CreateStoreRequest) Validate() error {
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" {
		return errors.New("name is required")
	}
	if len(r.Name) > 100 {
		return errors.New("name must be at most 100 characters")
	}
	return nil
}

// CreatePurchaseRequest is the expected JSON payload for POST
// /store-creditors/{id}/purchases. Amount and Fee are expressed in the
// wallet's major currency unit (matching CreateTransactionRequest in
// internal/transaction) and converted to cents via AmountCents/FeeCents.
type CreatePurchaseRequest struct {
	Amount      float64   `json:"amount"`
	Fee         float64   `json:"fee"`
	CategoryID  *uint     `json:"category_id"`
	Description string    `json:"description"`
	Date        time.Time `json:"date"`
}

// Validate normalizes and applies shape rules that don't need a
// database round trip (category existence/type checks happen in the
// handler, via category.ValidateType).
func (r *CreatePurchaseRequest) Validate() error {
	r.Description = strings.TrimSpace(r.Description)

	if math.IsNaN(r.Amount) || math.IsInf(r.Amount, 0) || r.Amount <= 0 {
		return errors.New("amount must be a positive, finite number")
	}
	if math.IsNaN(r.Fee) || math.IsInf(r.Fee, 0) || r.Fee < 0 {
		return errors.New("fee must be a non-negative, finite number")
	}
	if len(r.Description) > 255 {
		return errors.New("description must be at most 255 characters")
	}
	if r.CategoryID == nil {
		return errors.New("category_id is required")
	}
	if r.Date.IsZero() {
		return errors.New("date is required")
	}

	return nil
}

// AmountCents converts the human-entered Amount into an integer number
// of cents, rounding to the nearest cent to avoid floating point drift.
func (r *CreatePurchaseRequest) AmountCents() int64 {
	return int64(math.Round(r.Amount * 100))
}

// FeeCents converts the human-entered Fee into an integer number of
// cents, rounding to the nearest cent.
func (r *CreatePurchaseRequest) FeeCents() int64 {
	return int64(math.Round(r.Fee * 100))
}

// CreateSettlementRequest is the expected JSON payload for POST
// /store-creditors/{id}/settlements. AmountPaid and Fee are expressed in
// the wallet's major currency unit, converted to cents via
// AmountPaidCents/FeeCents.
type CreateSettlementRequest struct {
	AccountID  *uint     `json:"account_id"`
	AmountPaid float64   `json:"amount_paid"`
	Fee        float64   `json:"fee"`
	Date       time.Time `json:"date"`
}

// Validate normalizes and applies shape rules. Whether AmountPaid
// exceeds the shop's own OutstandingDebt can only be checked in the
// handler, once the real Creditor row has been locked and read.
func (r *CreateSettlementRequest) Validate() error {
	if r.AccountID == nil {
		return errors.New("account_id is required")
	}
	if math.IsNaN(r.AmountPaid) || math.IsInf(r.AmountPaid, 0) || r.AmountPaid <= 0 {
		return errors.New("amount_paid must be a positive, finite number")
	}
	if math.IsNaN(r.Fee) || math.IsInf(r.Fee, 0) || r.Fee < 0 {
		return errors.New("fee must be a non-negative, finite number")
	}
	if r.Date.IsZero() {
		return errors.New("date is required")
	}

	return nil
}

// AmountPaidCents converts the human-entered AmountPaid into an integer
// number of cents, rounding to the nearest cent.
func (r *CreateSettlementRequest) AmountPaidCents() int64 {
	return int64(math.Round(r.AmountPaid * 100))
}

// FeeCents converts the human-entered Fee into an integer number of
// cents, rounding to the nearest cent.
func (r *CreateSettlementRequest) FeeCents() int64 {
	return int64(math.Round(r.Fee * 100))
}

// CreditorResponse is the JSON shape returned to clients for a single
// shop. OutstandingDebt stays in cents on the wire, same convention as
// AccountResponse.Balance.
type CreditorResponse struct {
	ID              uint      `json:"id"`
	Name            string    `json:"name"`
	OutstandingDebt int64     `json:"outstanding_debt"`
	IsActive        bool      `json:"is_active"`
	CreatedAt       time.Time `json:"created_at"`
}

func toCreditorResponse(c Creditor) CreditorResponse {
	return CreditorResponse{
		ID:              c.ID,
		Name:            c.Name,
		OutstandingDebt: c.OutstandingDebt,
		IsActive:        c.IsActive,
		CreatedAt:       c.CreatedAt,
	}
}

// PurchaseResponse is the JSON shape returned to clients for a single
// credit purchase. Category is nil unless the handler preloaded it — the
// frontend renders purchase.category?.name instead of resolving
// CategoryID against a separately fetched category list.
type PurchaseResponse struct {
	ID          uint               `json:"id"`
	CreditorID  uint               `json:"creditor_id"`
	Amount      int64              `json:"amount"`
	Fee         int64              `json:"fee"`
	CategoryID  uint               `json:"category_id"`
	Category    *category.Category `json:"category"`
	Description string             `json:"description"`
	Date        time.Time          `json:"date"`
	CreatedAt   time.Time          `json:"created_at"`
}

func toPurchaseResponse(p Purchase) PurchaseResponse {
	return PurchaseResponse{
		ID:          p.ID,
		CreditorID:  p.CreditorID,
		Amount:      p.Amount,
		Fee:         p.Fee,
		CategoryID:  p.CategoryID,
		Category:    p.categoryOrNil(),
		Description: p.Description,
		Date:        p.Date,
		CreatedAt:   p.CreatedAt,
	}
}

// SettlementResponse is the JSON shape returned to clients for a single
// payment against a shop's tab. Account is nil unless the handler
// preloaded it — the frontend renders settlement.account?.name instead
// of resolving AccountID against a separately fetched wallet list.
type SettlementResponse struct {
	ID         uint             `json:"id"`
	CreditorID uint             `json:"creditor_id"`
	AccountID  uint             `json:"account_id"`
	Account    *account.Account `json:"account"`
	AmountPaid int64            `json:"amount_paid"`
	Fee        int64            `json:"fee"`
	Date       time.Time        `json:"date"`
	CreatedAt  time.Time        `json:"created_at"`
}

func toSettlementResponse(s Settlement) SettlementResponse {
	return SettlementResponse{
		ID:         s.ID,
		CreditorID: s.CreditorID,
		AccountID:  s.AccountID,
		Account:    s.accountOrNil(),
		AmountPaid: s.AmountPaid,
		Fee:        s.Fee,
		Date:       s.Date,
		CreatedAt:  s.CreatedAt,
	}
}
