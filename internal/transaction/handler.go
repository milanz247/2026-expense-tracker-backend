package transaction

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"gorm.io/gorm"

	"github.com/milan/expense-tracker/backend/internal/account"
	"github.com/milan/expense-tracker/backend/internal/category"
	"github.com/milan/expense-tracker/backend/internal/middleware"
)

// errFeeExceedsAmount is the one sentinel that stays local to this
// package — it's specific to how CreateTransactionHandler nets a fee
// off income, not a wallet-balance or category rule other packages
// need to share. Every other sentinel this handler checks against
// (account.ErrNotFound, account.ErrInactive, account.ErrInsufficientFunds,
// account.ErrCreditLimitExceeded, category.ErrNotFound,
// category.ErrTypeMismatch) is exported from the package that owns that
// invariant — see internal/account/balance.go and
// internal/category/validate.go — so internal/debt and internal/store
// enforce the exact same rules rather than each reimplementing them.
var errFeeExceedsAmount = errors.New("fee exceeds amount")

// Handler exposes HTTP endpoints for transaction management. All
// dependencies are injected explicitly via NewHandler.
type Handler struct {
	db     *gorm.DB
	logger *slog.Logger
}

// NewHandler wires a *gorm.DB and *slog.Logger into a new transaction Handler.
func NewHandler(db *gorm.DB, logger *slog.Logger) *Handler {
	return &Handler{db: db, logger: logger}
}

// RegisterRoutes attaches the handler's endpoints to the given mux under
// the supplied prefix (e.g. "/api/v1"). Both routes require a valid JWT.
func (h *Handler) RegisterRoutes(mux *http.ServeMux, prefix string, requireAuth func(http.Handler) http.Handler) {
	mux.Handle("POST "+prefix+"/transactions", requireAuth(http.HandlerFunc(h.CreateTransactionHandler)))
	mux.Handle("GET "+prefix+"/transactions", requireAuth(http.HandlerFunc(h.ListTransactionsHandler)))
}

// CreateTransactionHandler handles POST /transactions. The entire
// operation — locking the involved wallet(s), checking ownership and
// active status, checking balance, adjusting balances, and inserting
// the ledger row — runs inside a single GORM transaction (db.Transaction)
// so a mid-way failure (insufficient balance, a DB error, anything)
// rolls back every change atomically. No partial transfers, ever.
func (h *Handler) CreateTransactionHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req CreateTransactionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "request body is not valid JSON")
		return
	}

	if err := req.Validate(); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	// Category/type cross-check happens here, before the balance-adjusting
	// GORM transaction begins: it's a request-shape concern (does the
	// referenced category even make sense for this transaction?), not
	// something that needs a row lock or participates in the
	// balance-adjustment rollback. Validate has already nulled CategoryID
	// out for a transfer, so this only ever runs for income/expense.
	if req.CategoryID != nil {
		if err := category.ValidateType(r.Context(), h.db, userID, *req.CategoryID, req.Type); err != nil {
			switch {
			case errors.Is(err, category.ErrNotFound):
				writeError(w, http.StatusNotFound, "category not found")
			case errors.Is(err, category.ErrTypeMismatch):
				writeError(w, http.StatusBadRequest, "Invalid category type for this transaction")
			default:
				h.logger.Error("failed to validate transaction category", "error", err)
				writeError(w, http.StatusInternalServerError, "failed to create transaction")
			}
			return
		}
	}

	amountCents := req.AmountCents()
	feeCents := req.FeeCents()

	var created Transaction

	err := h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		switch req.Type {
		case TypeIncome:
			acct, err := account.LockOwnedActive(tx, userID, *req.AccountID)
			if err != nil {
				return err
			}
			// A fee on income (e.g. a bank's deposit fee) reduces what
			// actually lands in the wallet — the account is credited
			// net of fee, not the full amount.
			netCredit := amountCents - feeCents
			if netCredit < 0 {
				return errFeeExceedsAmount
			}
			if err := account.AdjustBalance(tx, acct.ID, netCredit); err != nil {
				return err
			}

		case TypeExpense:
			acct, err := account.LockOwnedActive(tx, userID, *req.AccountID)
			if err != nil {
				return err
			}
			total := amountCents + feeCents
			if err := account.ValidateOutgoing(acct, total); err != nil {
				return err
			}
			if err := account.AdjustBalance(tx, acct.ID, -total); err != nil {
				return err
			}

		case TypeTransfer:
			source, err := account.LockOwnedActive(tx, userID, *req.SourceAccountID)
			if err != nil {
				return err
			}
			dest, err := account.LockOwnedActive(tx, userID, *req.DestinationAccountID)
			if err != nil {
				return err
			}

			total := amountCents + feeCents
			if err := account.ValidateOutgoing(source, total); err != nil {
				return err
			}
			// Source is debited amount + fee; destination only ever
			// receives amount — the fee is the cost of moving the
			// money and doesn't land anywhere within the ledger. The
			// destination side is never balance-checked: crediting a
			// wallet (paying down a credit card included) is always
			// allowed, even past zero into a positive/overpaid balance.
			if err := account.AdjustBalance(tx, source.ID, -total); err != nil {
				return err
			}
			if err := account.AdjustBalance(tx, dest.ID, amountCents); err != nil {
				return err
			}

			// A transfer never carries a user-chosen category (Validate
			// already nulled req.CategoryID out — see its doc comment),
			// but it shouldn't render as "Uncategorized" in the ledger
			// either: auto-assign the system "Transfer" category so
			// every transfer displays a real badge, matching the same
			// convention internal/debt and internal/store use for their
			// own system-generated ledger entries.
			transferCategoryID, err := category.LookupSystemID(r.Context(), tx, category.SystemTransfer)
			if err != nil {
				return err
			}
			req.CategoryID = &transferCategoryID
		}

		newTxn := Transaction{
			UserID:               userID,
			Type:                 req.Type,
			Amount:               amountCents,
			Fee:                  feeCents,
			AccountID:            req.AccountID,
			SourceAccountID:      req.SourceAccountID,
			DestinationAccountID: req.DestinationAccountID,
			CategoryID:           req.CategoryID,
			Description:          req.Description,
			Date:                 req.Date,
		}
		if err := tx.Create(&newTxn).Error; err != nil {
			return err
		}
		created = newTxn
		return nil
	})

	if err != nil {
		switch {
		case errors.Is(err, account.ErrNotFound):
			writeError(w, http.StatusNotFound, err.Error())
		case errors.Is(err, account.ErrInactive):
			writeError(w, http.StatusUnprocessableEntity, err.Error()+" and cannot be used for new transactions")
		case errors.Is(err, account.ErrInsufficientFunds):
			writeError(w, http.StatusUnprocessableEntity, "insufficient funds")
		case errors.Is(err, account.ErrCreditLimitExceeded):
			writeError(w, http.StatusUnprocessableEntity, "credit limit exceeded")
		case errors.Is(err, errFeeExceedsAmount):
			writeError(w, http.StatusUnprocessableEntity, "fee cannot exceed amount")
		default:
			h.logger.Error("failed to create transaction", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to create transaction")
		}
		return
	}

	// Reload with associations preloaded so the response embeds the full
	// Category/Account objects, not just their IDs — same shape as
	// ListTransactionsHandler returns.
	if err := h.db.WithContext(r.Context()).
		Preload("Category").
		Preload("Account").
		Preload("SourceAccount").
		Preload("DestinationAccount").
		First(&created, created.ID).Error; err != nil {
		h.logger.Error("failed to reload created transaction", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create transaction")
		return
	}

	writeJSON(w, http.StatusCreated, toResponse(created))
}

// ListTransactionsHandler handles GET /transactions. It returns the
// authenticated caller's transaction history, most recent first, always
// scoped strictly by UserID.
//
// An optional ?account_id=<id> query parameter narrows the result to
// only transactions touching that one wallet — matching AccountID (an
// income/expense entry against it), SourceAccountID, or
// DestinationAccountID (either side of a transfer involving it). This
// is what powers the wallets page's per-wallet "Account Statement"
// view, so it's built as a single indexed WHERE clause (all three
// columns are indexed — see internal/transaction/model.go) rather than
// fetching everything and filtering in Go, which would not scale as a
// user's transaction history grows.
//
// Security note: the account_id filter needs no separate ownership
// check. Every transaction row already has user_id set to its creator,
// and every account a transaction can reference was verified to belong
// to that same user at creation time (see account.LockOwnedActive) — so
// "user_id = ? AND (... = account_id ...)" can never surface another
// user's data. Passing an account_id that doesn't exist, or belongs to
// someone else, simply yields an empty list rather than an error,
// which also avoids confirming or denying that an unrelated ID exists.
func (h *Handler) ListTransactionsHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	query := h.db.WithContext(r.Context()).Where("user_id = ?", userID)

	if rawAccountID := r.URL.Query().Get("account_id"); rawAccountID != "" {
		accountID, err := strconv.ParseUint(rawAccountID, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "account_id must be a positive integer")
			return
		}
		query = query.Where(
			"account_id = ? OR source_account_id = ? OR destination_account_id = ?",
			accountID, accountID, accountID,
		)
	}

	var transactions []Transaction
	if err := query.
		Preload("Category").
		Preload("Account").
		Preload("SourceAccount").
		Preload("DestinationAccount").
		Order("date desc, id desc").
		Find(&transactions).Error; err != nil {
		h.logger.Error("failed to list transactions", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list transactions")
		return
	}

	resp := make([]TransactionResponse, len(transactions))
	for i, t := range transactions {
		resp[i] = toResponse(t)
	}

	writeJSON(w, http.StatusOK, resp)
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
