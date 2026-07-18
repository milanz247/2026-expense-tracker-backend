package account

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// Supported wallet types. Kept as string constants (rather than an
// integer enum) so the value round-trips through JSON in a
// human-readable, self-describing form.
const (
	TypeBank       = "bank"
	TypeCash       = "cash"
	TypeCreditCard = "credit_card"
	TypeInvestment = "investment"
)

var validAccountTypes = map[string]bool{
	TypeBank:       true,
	TypeCash:       true,
	TypeCreditCard: true,
	TypeInvestment: true,
}

// normalizeName trims and length-checks a wallet name. Shared by both
// CreateAccountRequest and UpdateAccountRequest so the two never drift.
func normalizeName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("name is required")
	}
	if len(name) > 100 {
		return "", errors.New("name must be at most 100 characters")
	}
	return name, nil
}

// normalizeType lowercases and validates a wallet type against the
// supported set. Shared by both CreateAccountRequest and
// UpdateAccountRequest.
func normalizeType(t string) (string, error) {
	t = strings.ToLower(strings.TrimSpace(t))
	if !validAccountTypes[t] {
		return "", fmt.Errorf("type must be one of: %s, %s, %s, %s", TypeBank, TypeCash, TypeCreditCard, TypeInvestment)
	}
	return t, nil
}

// CreateAccountRequest is the expected JSON payload for POST /accounts.
// InitialBalance and CreditLimit are expressed in the wallet's major
// currency unit (e.g. 1500.50 for LKR 1,500.50), matching what a user
// would type into a form — both are converted to integer cent amounts
// via BalanceCents/CreditLimitCents.
type CreateAccountRequest struct {
	Name           string  `json:"name"`
	Type           string  `json:"type"`
	InitialBalance float64 `json:"initial_balance"`
	// CreditLimit is only meaningful when Type is TypeCreditCard. It is
	// silently normalized to 0 for every other type in Validate — never
	// trust the client to have hidden the field on its own.
	CreditLimit float64 `json:"credit_limit"`
}

// Validate normalizes and applies basic business rules to the incoming
// payload, including the domain invariants for each account type: cash,
// bank, and investment wallets are positive-asset accounts and can
// never start negative; a credit card is a liability account and may
// start with existing debt, but never further in debt than its own
// credit limit allows. It returns a descriptive error suitable for
// returning directly to the client on failure.
func (r *CreateAccountRequest) Validate() error {
	name, err := normalizeName(r.Name)
	if err != nil {
		return err
	}
	r.Name = name

	t, err := normalizeType(r.Type)
	if err != nil {
		return err
	}
	r.Type = t

	if math.IsNaN(r.InitialBalance) || math.IsInf(r.InitialBalance, 0) {
		return errors.New("initial_balance must be a finite number")
	}

	if r.Type != TypeCreditCard {
		r.CreditLimit = 0
		if r.InitialBalance < 0 {
			return errors.New("initial_balance cannot be negative for this wallet type")
		}
		return nil
	}

	if math.IsNaN(r.CreditLimit) || math.IsInf(r.CreditLimit, 0) || r.CreditLimit < 0 {
		return errors.New("credit_limit must be a non-negative, finite number")
	}
	if r.InitialBalance < -r.CreditLimit {
		return errors.New("initial_balance cannot be lower than -credit_limit")
	}

	return nil
}

// BalanceCents converts the human-entered InitialBalance into an integer
// number of cents, rounding to the nearest cent to avoid floating point
// drift (e.g. 19.99 * 100 landing on 1998.9999999999998).
func (r *CreateAccountRequest) BalanceCents() int64 {
	return int64(math.Round(r.InitialBalance * 100))
}

// CreditLimitCents converts the human-entered CreditLimit into an
// integer number of cents, rounding to the nearest cent.
func (r *CreateAccountRequest) CreditLimitCents() int64 {
	return int64(math.Round(r.CreditLimit * 100))
}

// UpdateAccountRequest is the expected JSON payload for PUT
// /accounts/{id}. Only Name and Type are editable this way — Balance
// only ever changes through transactions (see internal/transaction), so
// there is deliberately no way to overwrite it directly here.
type UpdateAccountRequest struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// Validate normalizes and applies the same rules as CreateAccountRequest.
func (r *UpdateAccountRequest) Validate() error {
	name, err := normalizeName(r.Name)
	if err != nil {
		return err
	}
	r.Name = name

	t, err := normalizeType(r.Type)
	if err != nil {
		return err
	}
	r.Type = t

	return nil
}

// AccountResponse is the JSON shape returned to clients for a single
// wallet. Balance stays in cents on the wire — the frontend divides by
// 100 and formats it for display, keeping the money representation
// exact end to end.
type AccountResponse struct {
	ID          uint      `json:"id"`
	Name        string    `json:"name"`
	Type        string    `json:"type"`
	Balance     int64     `json:"balance"`
	CreditLimit int64     `json:"credit_limit"`
	IsActive    bool      `json:"is_active"`
	CreatedAt   time.Time `json:"created_at"`
}

func toResponse(a Account) AccountResponse {
	return AccountResponse{
		ID:          a.ID,
		Name:        a.Name,
		Type:        a.Type,
		Balance:     a.Balance,
		CreditLimit: a.CreditLimit,
		IsActive:    a.IsActive,
		CreatedAt:   a.CreatedAt,
	}
}
