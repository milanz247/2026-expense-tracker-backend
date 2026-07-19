package account

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"gorm.io/gorm"

	"github.com/milan/expense-tracker/backend/internal/middleware"
)

// Handler exposes HTTP endpoints for wallet/account management. All
// dependencies are injected explicitly via NewHandler — there are no
// package-level globals, which keeps the handler testable in isolation.
type Handler struct {
	db     *gorm.DB
	logger *slog.Logger
}

// NewHandler wires a *gorm.DB and *slog.Logger into a new account Handler.
func NewHandler(db *gorm.DB, logger *slog.Logger) *Handler {
	return &Handler{db: db, logger: logger}
}

// RegisterRoutes attaches the handler's endpoints to the given mux under
// the supplied prefix (e.g. "/api/v1"). requireAuth is the JWT middleware
// (see internal/middleware) that both routes are wrapped with — accounts
// are always scoped to the authenticated caller, so there is no
// unauthenticated path into this handler.
func (h *Handler) RegisterRoutes(mux *http.ServeMux, prefix string, requireAuth func(http.Handler) http.Handler) {
	mux.Handle("POST "+prefix+"/accounts", requireAuth(http.HandlerFunc(h.CreateAccountHandler)))
	mux.Handle("GET "+prefix+"/accounts", requireAuth(http.HandlerFunc(h.ListAccountsHandler)))
	mux.Handle("PUT "+prefix+"/accounts/{id}", requireAuth(http.HandlerFunc(h.UpdateAccountHandler)))
	mux.Handle("DELETE "+prefix+"/accounts/{id}", requireAuth(http.HandlerFunc(h.ArchiveAccountHandler)))
}

// CreateAccountHandler handles POST /accounts. It decodes and validates
// the request body, attaches the authenticated caller's UserID (never
// trusting one supplied by the client), and persists the new wallet.
func (h *Handler) CreateAccountHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req CreateAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "request body is not valid JSON")
		return
	}

	if err := req.Validate(); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	newAccount := Account{
		UserID:        userID,
		Name:          req.Name,
		Type:          req.Type,
		Balance:       req.BalanceCents(),
		CreditLimit:   req.CreditLimitCents(),
		BranchName:    asOptional(req.BranchName),
		AccountNumber: asOptional(req.AccountNumber),
		HolderName:    asOptional(req.HolderName),
	}

	if err := h.db.WithContext(r.Context()).Create(&newAccount).Error; err != nil {
		h.logger.Error("failed to create account", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create wallet")
		return
	}

	writeJSON(w, http.StatusCreated, toResponse(newAccount))
}

// ListAccountsHandler handles GET /accounts. It returns every wallet
// belonging to the authenticated caller — active and archived alike, so
// archived wallets stay visible for historical ledger records — scoped
// strictly by UserID so a user can never see another user's accounts.
// Callers that need to build a "pick a wallet" form (e.g. the add
// transaction dialog) are responsible for filtering out archived ones
// client-side using is_active.
func (h *Handler) ListAccountsHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var accounts []Account
	if err := h.db.WithContext(r.Context()).
		Where("user_id = ?", userID).
		Order("created_at asc").
		Find(&accounts).Error; err != nil {
		h.logger.Error("failed to list accounts", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list wallets")
		return
	}

	resp := make([]AccountResponse, len(accounts))
	for i, a := range accounts {
		resp[i] = toResponse(a)
	}

	writeJSON(w, http.StatusOK, resp)
}

// UpdateAccountHandler handles PUT /accounts/{id}. It updates a wallet's
// Name and Type only — Balance is never writable through this endpoint,
// since it must only ever move through internal/transaction's atomic,
// balance-checked logic.
func (h *Handler) UpdateAccountHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	accountID, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid wallet id")
		return
	}

	var req UpdateAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "request body is not valid JSON")
		return
	}

	if err := req.Validate(); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	// Scoping the WHERE clause by user_id (not just id) means a request
	// for another user's wallet updates zero rows rather than someone
	// else's data — the query itself enforces ownership.
	result := h.db.WithContext(r.Context()).
		Model(&Account{}).
		Where("id = ? AND user_id = ?", accountID, userID).
		Updates(map[string]any{
			"name":           req.Name,
			"type":           req.Type,
			"branch_name":    asOptional(req.BranchName),
			"account_number": asOptional(req.AccountNumber),
			"holder_name":    asOptional(req.HolderName),
		})
	if result.Error != nil {
		h.logger.Error("failed to update wallet", "error", result.Error)
		writeError(w, http.StatusInternalServerError, "failed to update wallet")
		return
	}
	if result.RowsAffected == 0 {
		writeError(w, http.StatusNotFound, "wallet not found")
		return
	}

	var updated Account
	if err := h.db.WithContext(r.Context()).First(&updated, accountID).Error; err != nil {
		h.logger.Error("failed to reload wallet after update", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to update wallet")
		return
	}

	writeJSON(w, http.StatusOK, toResponse(updated))
}

// ArchiveAccountHandler handles DELETE /accounts/{id}. This is a soft
// delete: it flips IsActive to false and nothing else. The wallet, its
// balance, and every transaction that ever referenced it are left
// completely intact — archiving only hides it from forms for creating
// new transactions (see internal/transaction's lockOwnedActiveAccount).
func (h *Handler) ArchiveAccountHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	accountID, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid wallet id")
		return
	}

	result := h.db.WithContext(r.Context()).
		Model(&Account{}).
		Where("id = ? AND user_id = ?", accountID, userID).
		Update("is_active", false)
	if result.Error != nil {
		h.logger.Error("failed to archive wallet", "error", result.Error)
		writeError(w, http.StatusInternalServerError, "failed to archive wallet")
		return
	}
	if result.RowsAffected == 0 {
		writeError(w, http.StatusNotFound, "wallet not found")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"message": "wallet archived"})
}

// parseIDParam extracts and parses the {id} path value net/http's
// ServeMux populates for patterns like "PUT /accounts/{id}".
func parseIDParam(r *http.Request) (uint, error) {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		return 0, err
	}
	return uint(id), nil
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
