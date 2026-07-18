package user

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/milan/expense-tracker/backend/internal/middleware"
	"github.com/milan/expense-tracker/backend/pkg/auth"
)

// Handler exposes HTTP endpoints for user-related operations. All
// dependencies are injected explicitly via NewHandler — there are no
// package-level globals, which keeps the handler testable in isolation.
type Handler struct {
	db     *gorm.DB
	logger *slog.Logger
	tokens *auth.TokenManager
}

// NewHandler wires a *gorm.DB, *slog.Logger, and *auth.TokenManager into
// a new user Handler.
func NewHandler(db *gorm.DB, logger *slog.Logger, tokens *auth.TokenManager) *Handler {
	return &Handler{db: db, logger: logger, tokens: tokens}
}

// RegisterRoutes attaches the handler's endpoints to the given mux under
// the supplied prefix (e.g. "/api/v1"). Register and Login are public;
// the profile endpoints require a valid JWT via requireAuth since they
// read and mutate the caller's own account.
func (h *Handler) RegisterRoutes(mux *http.ServeMux, prefix string, requireAuth func(http.Handler) http.Handler) {
	mux.HandleFunc("POST "+prefix+"/register", h.RegisterHandler)
	mux.HandleFunc("POST "+prefix+"/login", h.LoginHandler)
	mux.Handle("GET "+prefix+"/profile", requireAuth(http.HandlerFunc(h.GetProfileHandler)))
	mux.Handle("PUT "+prefix+"/profile", requireAuth(http.HandlerFunc(h.UpdateProfileHandler)))
	mux.Handle("POST "+prefix+"/profile/password", requireAuth(http.HandlerFunc(h.ChangePasswordHandler)))
}

// RegisterHandler handles POST /register. It decodes and validates the
// request body, hashes the password, persists the new user, and returns
// the created resource (minus any sensitive fields).
func (h *Handler) RegisterHandler(w http.ResponseWriter, r *http.Request) {
	var req RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "request body is not valid JSON")
		return
	}

	if err := req.Validate(); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	passwordHash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		h.logger.Error("failed to hash password", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to process registration")
		return
	}

	newUser := User{
		Name:         req.Name,
		Email:        req.Email,
		PasswordHash: string(passwordHash),
		Currency:     req.Currency,
	}

	if err := h.db.WithContext(r.Context()).Create(&newUser).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			writeError(w, http.StatusConflict, "an account with this email already exists")
			return
		}
		h.logger.Error("failed to create user", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create user")
		return
	}

	resp := RegisterResponse{
		ID:       newUser.ID,
		Name:     newUser.Name,
		Email:    newUser.Email,
		Currency: newUser.Currency,
	}

	writeJSON(w, http.StatusCreated, resp)
}

// LoginHandler handles POST /login. It verifies the supplied credentials
// against the stored bcrypt hash and, on success, issues a signed JWT
// along with the minimal user info the frontend needs to render a
// personalized dashboard.
func (h *Handler) LoginHandler(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "request body is not valid JSON")
		return
	}

	if err := req.Validate(); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	var existing User
	err := h.db.WithContext(r.Context()).Where("email = ?", req.Email).First(&existing).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Same generic message as a bad password: do not reveal
			// whether the email is registered.
			writeError(w, http.StatusUnauthorized, "invalid email or password")
			return
		}
		h.logger.Error("failed to query user", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to process login")
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(existing.PasswordHash), []byte(req.Password)); err != nil {
		writeError(w, http.StatusUnauthorized, "invalid email or password")
		return
	}

	token, err := h.tokens.GenerateToken(existing.ID, existing.Name, existing.Email, existing.Currency)
	if err != nil {
		h.logger.Error("failed to generate token", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to process login")
		return
	}

	resp := LoginResponse{
		Token: token,
		User: AuthUser{
			Name:     existing.Name,
			Currency: existing.Currency,
		},
	}

	writeJSON(w, http.StatusOK, resp)
}

// GetProfileHandler handles GET /profile. It returns the authenticated
// caller's editable profile fields.
func (h *Handler) GetProfileHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var existing User
	if err := h.db.WithContext(r.Context()).First(&existing, userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		h.logger.Error("failed to load profile", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load profile")
		return
	}

	writeJSON(w, http.StatusOK, ProfileResponse{
		Name:     existing.Name,
		Email:    existing.Email,
		Currency: existing.Currency,
		Timezone: existing.Timezone,
	})
}

// UpdateProfileHandler handles PUT /profile. It updates the mutable
// profile fields (name, currency, timezone) for the authenticated
// caller. Email is intentionally not editable here — changing it has
// verification implications that are out of scope for this endpoint.
func (h *Handler) UpdateProfileHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req UpdateProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "request body is not valid JSON")
		return
	}

	if err := req.Validate(); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	if err := h.db.WithContext(r.Context()).
		Model(&User{}).
		Where("id = ?", userID).
		Updates(map[string]any{
			"name":     req.Name,
			"currency": req.Currency,
			"timezone": req.Timezone,
		}).Error; err != nil {
		h.logger.Error("failed to update profile", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to update profile")
		return
	}

	var updated User
	if err := h.db.WithContext(r.Context()).First(&updated, userID).Error; err != nil {
		h.logger.Error("failed to reload profile after update", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to update profile")
		return
	}

	writeJSON(w, http.StatusOK, ProfileResponse{
		Name:     updated.Name,
		Email:    updated.Email,
		Currency: updated.Currency,
		Timezone: updated.Timezone,
	})
}

// ChangePasswordHandler handles POST /profile/password. Security-critical:
// it re-verifies the caller's current password against the stored bcrypt
// hash before accepting a new one. A valid JWT alone is not sufficient
// proof of intent to change credentials — e.g. a session token that leaked
// (XSS, a shared machine) should not by itself be enough to lock the real
// account owner out by rotating their password.
func (h *Handler) ChangePasswordHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req ChangePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "request body is not valid JSON")
		return
	}

	if err := req.Validate(); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	var existing User
	if err := h.db.WithContext(r.Context()).First(&existing, userID).Error; err != nil {
		h.logger.Error("failed to load user for password change", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to change password")
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(existing.PasswordHash), []byte(req.OldPassword)); err != nil {
		// 401, not 422: this is an authentication failure (proving you
		// are who the token says you are), not a validation failure.
		writeError(w, http.StatusUnauthorized, "old password is incorrect")
		return
	}

	newHash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		h.logger.Error("failed to hash new password", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to change password")
		return
	}

	if err := h.db.WithContext(r.Context()).
		Model(&User{}).
		Where("id = ?", userID).
		Update("password_hash", string(newHash)).Error; err != nil {
		h.logger.Error("failed to persist new password", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to change password")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"message": "password updated successfully"})
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
