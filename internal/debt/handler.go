package debt

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/milan/expense-tracker/backend/internal/account"
	"github.com/milan/expense-tracker/backend/internal/category"
	"github.com/milan/expense-tracker/backend/internal/middleware"
	"github.com/milan/expense-tracker/backend/internal/transaction"
)

// Sentinel errors specific to this package's own invariants (as opposed
// to account.ErrNotFound/ErrInactive/ErrInsufficientFunds/
// ErrCreditLimitExceeded, which are reused as-is from internal/account).
var (
	errDebtNotFound     = errors.New("debt not found")
	errAlreadySettled   = errors.New("debt is already settled")
	errExceedsRemaining = errors.New("repayment_amount exceeds the debt's remaining amount")
)

// Handler exposes HTTP endpoints for debts and loans. All dependencies
// are injected explicitly via NewHandler.
type Handler struct {
	db     *gorm.DB
	logger *slog.Logger
}

// NewHandler wires a *gorm.DB and *slog.Logger into a new debt Handler.
func NewHandler(db *gorm.DB, logger *slog.Logger) *Handler {
	return &Handler{db: db, logger: logger}
}

// RegisterRoutes attaches the handler's endpoints to the given mux under
// the supplied prefix (e.g. "/api/v1"). Every route requires a valid JWT.
func (h *Handler) RegisterRoutes(mux *http.ServeMux, prefix string, requireAuth func(http.Handler) http.Handler) {
	mux.Handle("POST "+prefix+"/debts", requireAuth(http.HandlerFunc(h.CreateDebtHandler)))
	mux.Handle("GET "+prefix+"/debts", requireAuth(http.HandlerFunc(h.ListDebtsHandler)))
	mux.Handle("POST "+prefix+"/debts/{id}/repay", requireAuth(http.HandlerFunc(h.CreateRepaymentHandler)))
}

// CreateDebtHandler handles POST /debts. Recording a debt is a
// double-entry wallet movement, just like an ordinary transfer or
// expense: money lent out leaves the selected wallet right away (the
// caller actually handed over cash/made a transfer), and money borrowed
// in lands in the selected wallet right away. Both the wallet balance
// adjustment and the new Debt row are written inside a single GORM
// transaction (db.Transaction) so a mid-way failure rolls back atomically
// — no debt can ever exist without its matching wallet movement, or vice
// versa.
func (h *Handler) CreateDebtHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req CreateDebtRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "request body is not valid JSON")
		return
	}

	if err := req.Validate(); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	totalCents := req.TotalAmountCents()

	var created Debt

	err := h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		acct, err := account.LockOwnedActive(tx, userID, *req.AccountID)
		if err != nil {
			return err
		}

		var ledgerType, description, systemCategoryName string
		switch req.Type {
		case TypeLent:
			// Giving money away decreases the wallet, same rule as an
			// ordinary expense — it must not overdraw a positive-asset
			// wallet or exceed a credit card's limit.
			if err := account.ValidateOutgoing(acct, totalCents); err != nil {
				return err
			}
			if err := account.AdjustBalance(tx, acct.ID, -totalCents); err != nil {
				return err
			}
			ledgerType = transaction.TypeDebtLent
			description = fmt.Sprintf("Money lent to %s", req.PersonName)
			systemCategoryName = category.SystemDebtIssued

		case TypeBorrowed:
			// Receiving money always increases a wallet — no balance
			// check needed, same rule as the destination side of a
			// transfer or an income credit.
			if err := account.AdjustBalance(tx, acct.ID, totalCents); err != nil {
				return err
			}
			ledgerType = transaction.TypeDebtBorrowed
			description = fmt.Sprintf("Money borrowed from %s", req.PersonName)
			systemCategoryName = category.SystemDebtBorrowed
		}

		// Neither direction lets the caller pick a category — this ledger
		// entry is auto-assigned "Debt Issued" or "Debt Borrowed" instead
		// of being left "Uncategorized", the same convention
		// internal/transaction uses for a wallet transfer.
		debtCategoryID, err := category.LookupSystemID(r.Context(), tx, systemCategoryName)
		if err != nil {
			return err
		}

		newDebt := Debt{
			UserID:          userID,
			PersonName:      req.PersonName,
			Type:            req.Type,
			TotalAmount:     totalCents,
			RemainingAmount: totalCents,
			Status:          StatusPending,
			DueDate:         req.DueDate,
		}
		if err := tx.Create(&newDebt).Error; err != nil {
			return err
		}

		// The wallet-movement side of this debt is recorded as its own
		// ledger entry, using a type excluded from the public
		// POST /transactions allowlist (see
		// internal/transaction/dto.go) so it never gets double-counted
		// as ordinary income/expense in internal/report's monthly
		// aggregation — lending or borrowing money is not income or
		// spending.
		now := time.Now()
		ledgerEntry := transaction.Transaction{
			UserID:      userID,
			Type:        ledgerType,
			Amount:      totalCents,
			AccountID:   &acct.ID,
			CategoryID:  &debtCategoryID,
			Description: description,
			Date:        now,
		}
		if err := tx.Create(&ledgerEntry).Error; err != nil {
			return err
		}

		created = newDebt
		return nil
	})

	if err != nil {
		writeTransactionError(w, h.logger, err)
		return
	}

	writeJSON(w, http.StatusCreated, toResponse(created))
}

// CreateRepaymentHandler handles POST /debts/{id}/repay. Like
// CreateDebtHandler, the wallet balance adjustment, the Debt row's
// RemainingAmount/Status update, the Repayment history row, and the
// ledger entry are all written inside a single GORM transaction so a
// mid-way failure rolls back atomically.
func (h *Handler) CreateRepaymentHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	debtID, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "id must be a positive integer")
		return
	}

	var req CreateRepaymentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "request body is not valid JSON")
		return
	}

	if err := req.Validate(); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	repayCents := req.RepaymentAmountCents()

	var updated Debt

	err = h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		debt, err := lockOwnedDebt(tx, userID, uint(debtID))
		if err != nil {
			return err
		}
		if debt.Status == StatusSettled {
			return errAlreadySettled
		}
		if repayCents > debt.RemainingAmount {
			return errExceedsRemaining
		}

		acct, err := account.LockOwnedActive(tx, userID, *req.AccountID)
		if err != nil {
			return err
		}

		var ledgerType, description string
		switch debt.Type {
		case TypeLent:
			// Getting paid back increases the wallet — no balance check
			// needed, same as the borrowed-debt creation path above.
			if err := account.AdjustBalance(tx, acct.ID, repayCents); err != nil {
				return err
			}
			ledgerType = transaction.TypeRepaymentReceived
			description = fmt.Sprintf("Repayment received from %s", debt.PersonName)

		case TypeBorrowed:
			// Paying back decreases the wallet, same rule as an
			// ordinary expense.
			if err := account.ValidateOutgoing(acct, repayCents); err != nil {
				return err
			}
			if err := account.AdjustBalance(tx, acct.ID, -repayCents); err != nil {
				return err
			}
			ledgerType = transaction.TypeRepaymentPaid
			description = fmt.Sprintf("Repayment paid to %s", debt.PersonName)
		}

		newRemaining := debt.RemainingAmount - repayCents
		newStatus := debt.Status
		if newRemaining == 0 {
			newStatus = StatusSettled
		}
		if err := tx.Model(&Debt{}).Where("id = ?", debt.ID).Updates(map[string]any{
			"remaining_amount": newRemaining,
			"status":           newStatus,
		}).Error; err != nil {
			return err
		}

		repayment := Repayment{
			DebtID:    debt.ID,
			AccountID: acct.ID,
			Amount:    repayCents,
			Date:      req.Date,
		}
		if err := tx.Create(&repayment).Error; err != nil {
			return err
		}

		// A repayment never lets the caller pick a category either — the
		// same system "Repayment" category covers both directions
		// (received or paid), since from a categorization standpoint it's
		// the same kind of event regardless of which side of the debt the
		// caller is on.
		repaymentCategoryID, err := category.LookupSystemID(r.Context(), tx, category.SystemRepayment)
		if err != nil {
			return err
		}

		ledgerEntry := transaction.Transaction{
			UserID:      userID,
			Type:        ledgerType,
			Amount:      repayCents,
			AccountID:   &acct.ID,
			CategoryID:  &repaymentCategoryID,
			Description: description,
			Date:        req.Date,
		}
		if err := tx.Create(&ledgerEntry).Error; err != nil {
			return err
		}

		debt.RemainingAmount = newRemaining
		debt.Status = newStatus
		updated = debt
		return nil
	})

	if err != nil {
		writeTransactionError(w, h.logger, err)
		return
	}

	writeJSON(w, http.StatusCreated, toResponse(updated))
}

// ListDebtsHandler handles GET /debts. It returns the authenticated
// caller's debts, always scoped strictly by UserID, ordered by due date
// so the most urgent ones surface first — the frontend further splits
// this single list into "Money Lent" / "Money Borrowed" tabs client-side
// by Type.
func (h *Handler) ListDebtsHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var debts []Debt
	if err := h.db.WithContext(r.Context()).
		Where("user_id = ?", userID).
		Order("due_date asc").
		Find(&debts).Error; err != nil {
		h.logger.Error("failed to list debts", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list debts")
		return
	}

	resp := make([]DebtResponse, len(debts))
	for i, d := range debts {
		resp[i] = toResponse(d)
	}

	writeJSON(w, http.StatusOK, resp)
}

// lockOwnedDebt loads a debt by ID with a SELECT ... FOR UPDATE row lock
// so two concurrent repayments against the same debt can never both read
// the same RemainingAmount and both apply against it — the second
// request blocks until the first transaction commits or rolls back. Must
// be called with a *gorm.DB that is already inside a db.Transaction
// callback. It also enforces ownership, scoped by userID.
func lockOwnedDebt(tx *gorm.DB, userID, debtID uint) (Debt, error) {
	var d Debt
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND user_id = ?", debtID, userID).
		First(&d).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return Debt{}, errDebtNotFound
		}
		return Debt{}, err
	}
	return d, nil
}

// writeTransactionError maps every sentinel error either CreateDebtHandler
// or CreateRepaymentHandler's db.Transaction callback can return to the
// appropriate HTTP status, reusing internal/account's own sentinels so
// the same failure (e.g. insufficient funds) always produces the same
// response shape regardless of which package's handler is reporting it.
func writeTransactionError(w http.ResponseWriter, logger *slog.Logger, err error) {
	switch {
	case errors.Is(err, account.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, account.ErrInactive):
		writeError(w, http.StatusUnprocessableEntity, err.Error()+" and cannot be used for new transactions")
	case errors.Is(err, account.ErrInsufficientFunds):
		writeError(w, http.StatusUnprocessableEntity, "insufficient funds")
	case errors.Is(err, account.ErrCreditLimitExceeded):
		writeError(w, http.StatusUnprocessableEntity, "credit limit exceeded")
	case errors.Is(err, errDebtNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, errAlreadySettled):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, errExceedsRemaining):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	default:
		logger.Error("debt operation failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to process request")
	}
}

// writeJSON marshals payload as JSON and writes it to w with the given
// status code, setting the appropriate Content-Type header.
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("failed to write JSON response", "error", err)
	}
}

// errorResponse is the standard JSON shape returned for any handled error.
type errorResponse struct {
	Error string `json:"error"`
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, errorResponse{Error: message})
}
