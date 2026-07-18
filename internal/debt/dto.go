package debt

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

var validDebtTypes = map[string]bool{
	TypeLent:     true,
	TypeBorrowed: true,
}

// CreateDebtRequest is the expected JSON payload for POST /debts.
// TotalAmount is expressed in the wallet's major currency unit (matching
// CreateAccountRequest.InitialBalance in internal/account) and converted
// to cents via TotalAmountCents.
//
// AccountID names the wallet this debt's initial movement passes
// through: for a lent debt, the wallet money leaves; for a borrowed
// debt, the wallet money lands in. See handler.go's CreateDebtHandler
// for the balance arithmetic.
type CreateDebtRequest struct {
	PersonName  string    `json:"person_name"`
	Type        string    `json:"type"`
	TotalAmount float64   `json:"total_amount"`
	AccountID   *uint     `json:"account_id"`
	DueDate     time.Time `json:"due_date"`
}

// Validate normalizes and applies shape/business rules that don't need a
// database round trip (ownership/active-status/balance checks happen in
// the handler, inside the GORM transaction, since they require locking a
// real wallet row — see account.LockOwnedActive).
func (r *CreateDebtRequest) Validate() error {
	r.PersonName = strings.TrimSpace(r.PersonName)
	if r.PersonName == "" {
		return errors.New("person_name is required")
	}
	if len(r.PersonName) > 100 {
		return errors.New("person_name must be at most 100 characters")
	}

	r.Type = strings.ToLower(strings.TrimSpace(r.Type))
	if !validDebtTypes[r.Type] {
		return fmt.Errorf("type must be one of: %s, %s", TypeLent, TypeBorrowed)
	}

	if math.IsNaN(r.TotalAmount) || math.IsInf(r.TotalAmount, 0) || r.TotalAmount <= 0 {
		return errors.New("total_amount must be a positive, finite number")
	}

	if r.AccountID == nil {
		return errors.New("account_id is required")
	}

	if r.DueDate.IsZero() {
		return errors.New("due_date is required")
	}

	return nil
}

// TotalAmountCents converts the human-entered TotalAmount into an
// integer number of cents, rounding to the nearest cent to avoid
// floating point drift.
func (r *CreateDebtRequest) TotalAmountCents() int64 {
	return int64(math.Round(r.TotalAmount * 100))
}

// CreateRepaymentRequest is the expected JSON payload for POST
// /debts/{id}/repay. RepaymentAmount is expressed in the wallet's major
// currency unit, converted to cents via RepaymentAmountCents.
type CreateRepaymentRequest struct {
	RepaymentAmount float64   `json:"repayment_amount"`
	AccountID       *uint     `json:"account_id"`
	Date            time.Time `json:"date"`
}

// Validate normalizes and applies shape rules. Whether RepaymentAmount
// exceeds the debt's own RemainingAmount can only be checked in the
// handler, once the real Debt row has been locked and read.
func (r *CreateRepaymentRequest) Validate() error {
	if math.IsNaN(r.RepaymentAmount) || math.IsInf(r.RepaymentAmount, 0) || r.RepaymentAmount <= 0 {
		return errors.New("repayment_amount must be a positive, finite number")
	}

	if r.AccountID == nil {
		return errors.New("account_id is required")
	}

	if r.Date.IsZero() {
		return errors.New("date is required")
	}

	return nil
}

// RepaymentAmountCents converts the human-entered RepaymentAmount into
// an integer number of cents, rounding to the nearest cent.
func (r *CreateRepaymentRequest) RepaymentAmountCents() int64 {
	return int64(math.Round(r.RepaymentAmount * 100))
}

// DebtResponse is the JSON shape returned to clients for a single debt.
// TotalAmount/RemainingAmount stay in cents on the wire, same convention
// as AccountResponse.Balance.
type DebtResponse struct {
	ID              uint      `json:"id"`
	PersonName      string    `json:"person_name"`
	Type            string    `json:"type"`
	TotalAmount     int64     `json:"total_amount"`
	RemainingAmount int64     `json:"remaining_amount"`
	Status          string    `json:"status"`
	DueDate         time.Time `json:"due_date"`
	CreatedAt       time.Time `json:"created_at"`
}

func toResponse(d Debt) DebtResponse {
	return DebtResponse{
		ID:              d.ID,
		PersonName:      d.PersonName,
		Type:            d.Type,
		TotalAmount:     d.TotalAmount,
		RemainingAmount: d.RemainingAmount,
		Status:          d.Status,
		DueDate:         d.DueDate,
		CreatedAt:       d.CreatedAt,
	}
}
