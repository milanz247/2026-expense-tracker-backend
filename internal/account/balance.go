package account

import (
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Sentinel errors for wallet-balance operations. Exported so every
// package that moves money through a wallet (internal/transaction,
// internal/debt, internal/store, ...) can map the same failure to the
// same HTTP response, rather than each reimplementing — and each
// potentially drifting from — this app's core balance-integrity rules.
var (
	ErrNotFound            = errors.New("wallet not found")
	ErrInactive            = errors.New("wallet is archived")
	ErrInsufficientFunds   = errors.New("insufficient funds")
	ErrCreditLimitExceeded = errors.New("credit limit exceeded")
)

// LockOwnedActive loads a wallet by ID with a SELECT ... FOR UPDATE row
// lock (via clause.Locking) so two concurrent requests against the same
// wallet can never both read a balance, both pass a sufficiency check,
// and both deduct — the second request blocks until the first
// transaction commits or rolls back. Must be called with a *gorm.DB
// that is already inside a db.Transaction callback; calling it outside
// one takes the row lock for the duration of a single statement, which
// defeats the purpose.
//
// It also enforces ownership (scoped by userID) and active status —
// the two invariants every caller in this codebase requires before a
// wallet can be touched by a new movement of money.
func LockOwnedActive(tx *gorm.DB, userID, accountID uint) (Account, error) {
	var acct Account
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND user_id = ?", accountID, userID).
		First(&acct).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return Account{}, ErrNotFound
		}
		return Account{}, err
	}
	if !acct.IsActive {
		return Account{}, ErrInactive
	}
	return acct, nil
}

// ValidateOutgoing enforces the domain rule for money leaving acct (an
// expense debit, the source side of a transfer, lending money out, or
// paying down a borrowed debt/store tab — anything that reduces the
// balance) before any balance is touched. The rule depends on what the
// account represents:
//
//   - Cash, Bank, Investment (positive-asset accounts): the balance can
//     never go negative — you cannot spend money you don't have. Checked
//     as acct.Balance >= total, equivalently newBalance >= 0.
//   - Credit Card (a liability/debt account): Balance is already
//     negative debt (e.g. -15000 = 150.00 owed), and spending makes it
//     more negative. It's allowed to go further into debt, but never
//     past the wallet's own CreditLimit: newBalance >= -CreditLimit.
//
// total is the full amount leaving the wallet in cents (e.g. amount +
// fee), matching NewBalance = CurrentBalance - total.
func ValidateOutgoing(acct Account, total int64) error {
	if acct.Type == TypeCreditCard {
		newBalance := acct.Balance - total
		if newBalance < -acct.CreditLimit {
			return ErrCreditLimitExceeded
		}
		return nil
	}

	if acct.Balance < total {
		return ErrInsufficientFunds
	}
	return nil
}

// AdjustBalance applies a signed delta to a wallet's balance with a
// single atomic SQL statement (balance = balance + ?), inside the
// caller's already-locked transaction. Positive deltaCents credits the
// wallet (always allowed, no check needed — see every caller's own
// comments for why); negative deltaCents debits it and should only be
// called after ValidateOutgoing has passed for the same total.
func AdjustBalance(tx *gorm.DB, accountID uint, deltaCents int64) error {
	return tx.Model(&Account{}).
		Where("id = ?", accountID).
		Update("balance", gorm.Expr("balance + ?", deltaCents)).Error
}
