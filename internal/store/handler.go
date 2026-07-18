package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/milan/expense-tracker/backend/internal/account"
	"github.com/milan/expense-tracker/backend/internal/category"
	"github.com/milan/expense-tracker/backend/internal/middleware"
	"github.com/milan/expense-tracker/backend/internal/transaction"
)

// Sentinel errors specific to this package's own invariants (as opposed
// to account.ErrNotFound/ErrInactive/ErrInsufficientFunds/
// ErrCreditLimitExceeded and category.ErrNotFound/ErrTypeMismatch, which
// are reused as-is from their owning packages).
var (
	errCreditorNotFound   = errors.New("store not found")
	errExceedsOutstanding = errors.New("amount_paid exceeds the shop's outstanding debt")
)

// Handler exposes HTTP endpoints for store tabs (persistent local-shop
// running credit). All dependencies are injected explicitly via
// NewHandler.
type Handler struct {
	db     *gorm.DB
	logger *slog.Logger
}

// NewHandler wires a *gorm.DB and *slog.Logger into a new store Handler.
func NewHandler(db *gorm.DB, logger *slog.Logger) *Handler {
	return &Handler{db: db, logger: logger}
}

// RegisterRoutes attaches the handler's endpoints to the given mux under
// the supplied prefix (e.g. "/api/v1"). Every route requires a valid JWT.
func (h *Handler) RegisterRoutes(mux *http.ServeMux, prefix string, requireAuth func(http.Handler) http.Handler) {
	mux.Handle("POST "+prefix+"/store-creditors", requireAuth(http.HandlerFunc(h.CreateStoreHandler)))
	mux.Handle("GET "+prefix+"/store-creditors", requireAuth(http.HandlerFunc(h.ListStoresHandler)))
	mux.Handle("POST "+prefix+"/store-creditors/{id}/purchases", requireAuth(http.HandlerFunc(h.RecordStorePurchaseHandler)))
	mux.Handle("GET "+prefix+"/store-creditors/{id}/purchases", requireAuth(http.HandlerFunc(h.ListStorePurchasesHandler)))
	mux.Handle("POST "+prefix+"/store-creditors/{id}/settlements", requireAuth(http.HandlerFunc(h.RecordStoreSettlementHandler)))
	mux.Handle("GET "+prefix+"/store-creditors/{id}/settlements", requireAuth(http.HandlerFunc(h.ListStoreSettlementsHandler)))
}

// CreateStoreHandler handles POST /store-creditors. A new shop always
// starts at zero OutstandingDebt and IsActive true — no wallet or ledger
// movement happens here, since a shop by itself isn't a transaction,
// only its purchases and settlements are.
func (h *Handler) CreateStoreHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req CreateStoreRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "request body is not valid JSON")
		return
	}
	if err := req.Validate(); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	creditor := Creditor{
		UserID:          userID,
		Name:            req.Name,
		OutstandingDebt: 0,
		IsActive:        true,
	}
	if err := h.db.WithContext(r.Context()).Create(&creditor).Error; err != nil {
		h.logger.Error("failed to create store", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create store")
		return
	}

	writeJSON(w, http.StatusCreated, toCreditorResponse(creditor))
}

// ListStoresHandler handles GET /store-creditors. It returns the
// authenticated caller's shops, always scoped strictly by UserID,
// alphabetically — this is the persistent left-column shop list the
// frontend renders every purchase/settlement dialog against.
func (h *Handler) ListStoresHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var creditors []Creditor
	if err := h.db.WithContext(r.Context()).
		Where("user_id = ?", userID).
		Order("name asc").
		Find(&creditors).Error; err != nil {
		h.logger.Error("failed to list stores", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list stores")
		return
	}

	resp := make([]CreditorResponse, len(creditors))
	for i, c := range creditors {
		resp[i] = toCreditorResponse(c)
	}
	writeJSON(w, http.StatusOK, resp)
}

// RecordStorePurchaseHandler handles POST /store-creditors/{id}/purchases.
// A credit purchase increases the shop's OutstandingDebt and
// simultaneously creates a global expense Transaction under the selected
// category, so the spend shows up in the caller's monthly expense
// reports immediately — not deferred until the tab is eventually
// settled. Both writes happen inside a single GORM transaction so a
// mid-way failure rolls back atomically. No wallet is touched here: a
// credit purchase is a promise to pay later, not an immediate cash
// movement (see internal/store/model.go's Creditor doc comment).
func (h *Handler) RecordStorePurchaseHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	creditorID, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "id must be a positive integer")
		return
	}

	var req CreatePurchaseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "request body is not valid JSON")
		return
	}
	if err := req.Validate(); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	if err := category.ValidateType(r.Context(), h.db, userID, *req.CategoryID, transaction.TypeExpense); err != nil {
		switch {
		case errors.Is(err, category.ErrNotFound):
			writeError(w, http.StatusNotFound, "category not found")
		case errors.Is(err, category.ErrTypeMismatch):
			writeError(w, http.StatusBadRequest, "Invalid category type for this transaction")
		default:
			h.logger.Error("failed to validate purchase category", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to record purchase")
		}
		return
	}

	amountCents := req.AmountCents()
	feeCents := req.FeeCents()
	// A purchase's fee (e.g. a delivery/service charge the shop adds) is
	// still money the caller now owes this shop and still spend against
	// the chosen category — unlike a settlement's fee, there is no
	// separate wallet to deduct it from at purchase time, so it is
	// folded into the same total rather than split into a second ledger
	// entry.
	total := amountCents + feeCents

	var created Purchase

	err = h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		creditor, err := lockOwnedCreditor(tx, userID, uint(creditorID))
		if err != nil {
			return err
		}

		if err := tx.Model(&Creditor{}).
			Where("id = ?", creditor.ID).
			Update("outstanding_debt", gorm.Expr("outstanding_debt + ?", total)).Error; err != nil {
			return err
		}

		ledgerEntry := transaction.Transaction{
			UserID:      userID,
			Type:        transaction.TypeExpense,
			Amount:      total,
			CategoryID:  req.CategoryID,
			Description: fmt.Sprintf("%s — %s", creditor.Name, req.Description),
			Date:        req.Date,
		}
		if err := tx.Create(&ledgerEntry).Error; err != nil {
			return err
		}

		purchase := Purchase{
			CreditorID:    creditor.ID,
			Amount:        amountCents,
			Fee:           feeCents,
			CategoryID:    *req.CategoryID,
			TransactionID: ledgerEntry.ID,
			Description:   req.Description,
			Date:          req.Date,
		}
		if err := tx.Create(&purchase).Error; err != nil {
			return err
		}

		created = purchase
		return nil
	})

	if err != nil {
		writeTransactionError(w, h.logger, err)
		return
	}

	writeJSON(w, http.StatusCreated, toPurchaseResponse(created))
}

// ListStorePurchasesHandler handles GET /store-creditors/{id}/purchases,
// scoped to a single shop the caller owns — the "Credit Purchases" tab's
// data source.
func (h *Handler) ListStorePurchasesHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	creditorID, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "id must be a positive integer")
		return
	}

	if err := h.checkOwnedCreditor(r, userID, uint(creditorID)); err != nil {
		writeTransactionError(w, h.logger, err)
		return
	}

	var purchases []Purchase
	if err := h.db.WithContext(r.Context()).
		Preload("Category").
		Where("creditor_id = ?", creditorID).
		Order("date desc, id desc").
		Find(&purchases).Error; err != nil {
		h.logger.Error("failed to list store purchases", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list purchases")
		return
	}

	resp := make([]PurchaseResponse, len(purchases))
	for i, p := range purchases {
		resp[i] = toPurchaseResponse(p)
	}
	writeJSON(w, http.StatusOK, resp)
}

// RecordStoreSettlementHandler handles POST
// /store-creditors/{id}/settlements. Paying down a shop's tab decreases
// both the shop's OutstandingDebt and the selected wallet's balance
// atomically. If a Fee is present (e.g. a bank transfer charge for
// making the payment), it is deducted from the wallet in addition to
// AmountPaid and logged as its own separate global expense transaction
// — kept apart from the principal repayment because the principal was
// already recognized as an expense back at purchase time (see
// RecordStorePurchaseHandler); re-logging it here would double-count it
// in monthly expense reports, whereas the fee is a brand new cost that
// has never been recorded anywhere else. If OutstandingDebt reaches
// zero the shop is left exactly as-is otherwise (IsActive stays true) —
// a "settled (OK)" billing state is a value the frontend derives from
// OutstandingDebt == 0, not a separate stored field, so the shop remains
// ready for its next purchase.
func (h *Handler) RecordStoreSettlementHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	creditorID, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "id must be a positive integer")
		return
	}

	var req CreateSettlementRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "request body is not valid JSON")
		return
	}
	if err := req.Validate(); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	amountPaidCents := req.AmountPaidCents()
	feeCents := req.FeeCents()

	var created Settlement

	err = h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		creditor, err := lockOwnedCreditor(tx, userID, uint(creditorID))
		if err != nil {
			return err
		}
		if amountPaidCents > creditor.OutstandingDebt {
			return errExceedsOutstanding
		}

		acct, err := account.LockOwnedActive(tx, userID, *req.AccountID)
		if err != nil {
			return err
		}

		total := amountPaidCents + feeCents
		if err := account.ValidateOutgoing(acct, total); err != nil {
			return err
		}
		if err := account.AdjustBalance(tx, acct.ID, -total); err != nil {
			return err
		}

		if err := tx.Model(&Creditor{}).
			Where("id = ?", creditor.ID).
			Update("outstanding_debt", gorm.Expr("outstanding_debt - ?", amountPaidCents)).Error; err != nil {
			return err
		}

		// Neither ledger entry below carries a user-chosen category — a
		// settlement never asks for one — so both are auto-assigned the
		// system "Store Tab Payment" category rather than left
		// "Uncategorized", the same convention internal/transaction uses
		// for a wallet transfer's ledger entry.
		storeCategoryID, err := category.LookupSystemID(r.Context(), tx, category.SystemStoreTabPayment)
		if err != nil {
			return err
		}

		// The principal payment uses a ledger type excluded from the
		// public POST /transactions allowlist (see
		// internal/transaction/dto.go), the same convention
		// internal/debt uses for repayments — it must never be counted
		// as a fresh expense, since the spend was already recognized at
		// purchase time.
		principalEntry := transaction.Transaction{
			UserID:      userID,
			Type:        transaction.TypeRepaymentPaid,
			Amount:      amountPaidCents,
			AccountID:   &acct.ID,
			CategoryID:  &storeCategoryID,
			Description: fmt.Sprintf("Store tab payment to %s", creditor.Name),
			Date:        req.Date,
		}
		if err := tx.Create(&principalEntry).Error; err != nil {
			return err
		}

		if feeCents > 0 {
			feeEntry := transaction.Transaction{
				UserID:      userID,
				Type:        transaction.TypeExpense,
				Amount:      feeCents,
				AccountID:   &acct.ID,
				CategoryID:  &storeCategoryID,
				Description: fmt.Sprintf("Payment fee — %s", creditor.Name),
				Date:        req.Date,
			}
			if err := tx.Create(&feeEntry).Error; err != nil {
				return err
			}
		}

		settlement := Settlement{
			CreditorID: creditor.ID,
			AccountID:  acct.ID,
			AmountPaid: amountPaidCents,
			Fee:        feeCents,
			Date:       req.Date,
		}
		if err := tx.Create(&settlement).Error; err != nil {
			return err
		}

		created = settlement
		return nil
	})

	if err != nil {
		writeTransactionError(w, h.logger, err)
		return
	}

	writeJSON(w, http.StatusCreated, toSettlementResponse(created))
}

// ListStoreSettlementsHandler handles GET
// /store-creditors/{id}/settlements, scoped to a single shop the caller
// owns — the "Payments History" tab's data source.
func (h *Handler) ListStoreSettlementsHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	creditorID, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "id must be a positive integer")
		return
	}

	if err := h.checkOwnedCreditor(r, userID, uint(creditorID)); err != nil {
		writeTransactionError(w, h.logger, err)
		return
	}

	var settlements []Settlement
	if err := h.db.WithContext(r.Context()).
		Preload("Account").
		Where("creditor_id = ?", creditorID).
		Order("date desc, id desc").
		Find(&settlements).Error; err != nil {
		h.logger.Error("failed to list store settlements", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list settlements")
		return
	}

	resp := make([]SettlementResponse, len(settlements))
	for i, s := range settlements {
		resp[i] = toSettlementResponse(s)
	}
	writeJSON(w, http.StatusOK, resp)
}

// checkOwnedCreditor confirms creditorID exists and belongs to userID,
// without taking a row lock — used by the two read-only list handlers,
// which have no concurrent-write race to guard against.
func (h *Handler) checkOwnedCreditor(r *http.Request, userID, creditorID uint) error {
	var creditor Creditor
	err := h.db.WithContext(r.Context()).
		Where("id = ? AND user_id = ?", creditorID, userID).
		First(&creditor).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errCreditorNotFound
		}
		return err
	}
	return nil
}

// lockOwnedCreditor loads a shop by ID with a SELECT ... FOR UPDATE row
// lock so two concurrent purchases/settlements against the same shop can
// never both read the same OutstandingDebt and both apply against it —
// the second request blocks until the first transaction commits or
// rolls back. Must be called with a *gorm.DB that is already inside a
// db.Transaction callback. It also enforces ownership, scoped by
// userID.
func lockOwnedCreditor(tx *gorm.DB, userID, creditorID uint) (Creditor, error) {
	var c Creditor
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND user_id = ?", creditorID, userID).
		First(&c).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return Creditor{}, errCreditorNotFound
		}
		return Creditor{}, err
	}
	return c, nil
}

// writeTransactionError maps every sentinel error this package's
// db.Transaction callbacks (or ownership pre-checks) can return to the
// appropriate HTTP status, reusing internal/account's and
// internal/category's own sentinels so the same failure always produces
// the same response shape regardless of which package's handler is
// reporting it.
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
	case errors.Is(err, errCreditorNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, errExceedsOutstanding):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	default:
		logger.Error("store operation failed", "error", err)
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
