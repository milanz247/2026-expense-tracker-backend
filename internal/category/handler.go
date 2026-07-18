package category

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"gorm.io/gorm"

	"github.com/milan/expense-tracker/backend/internal/middleware"
)

// Handler exposes HTTP endpoints for category management. All
// dependencies are injected explicitly via NewHandler — there are no
// package-level globals, which keeps the handler testable in isolation.
type Handler struct {
	db     *gorm.DB
	logger *slog.Logger
}

// NewHandler wires a *gorm.DB and *slog.Logger into a new category Handler.
func NewHandler(db *gorm.DB, logger *slog.Logger) *Handler {
	return &Handler{db: db, logger: logger}
}

// RegisterRoutes attaches the handler's endpoints to the given mux under
// the supplied prefix (e.g. "/api/v1"). requireAuth is the JWT
// middleware (see internal/middleware) every route is wrapped with.
func (h *Handler) RegisterRoutes(mux *http.ServeMux, prefix string, requireAuth func(http.Handler) http.Handler) {
	mux.Handle("POST "+prefix+"/categories", requireAuth(http.HandlerFunc(h.CreateCategoryHandler)))
	mux.Handle("GET "+prefix+"/categories", requireAuth(http.HandlerFunc(h.ListCategoriesHandler)))
	mux.Handle("PUT "+prefix+"/categories/{id}", requireAuth(http.HandlerFunc(h.UpdateCategoryHandler)))
	mux.Handle("DELETE "+prefix+"/categories/{id}", requireAuth(http.HandlerFunc(h.DeleteCategoryHandler)))
}

// CreateCategoryHandler handles POST /categories. It always creates a
// category owned by the authenticated caller (UserID set) — there is no
// way to create a global/system category through the API; those only
// ever come from pkg/database's startup seeding.
func (h *Handler) CreateCategoryHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req CreateCategoryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "request body is not valid JSON")
		return
	}

	if err := req.Validate(); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	newCategory := Category{
		UserID: &userID,
		Name:   req.Name,
		Type:   req.Type,
		Color:  req.Color,
		Icon:   req.Icon,
	}

	if err := h.db.WithContext(r.Context()).Create(&newCategory).Error; err != nil {
		h.logger.Error("failed to create category", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create category")
		return
	}

	writeJSON(w, http.StatusCreated, toResponse(newCategory))
}

// ListCategoriesHandler handles GET /categories. It returns every global
// system category (UserID IS NULL) plus the authenticated caller's own
// custom categories — never another user's custom categories.
func (h *Handler) ListCategoriesHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var categories []Category
	if err := h.db.WithContext(r.Context()).
		Where("user_id IS NULL OR user_id = ?", userID).
		Order("type asc, name asc").
		Find(&categories).Error; err != nil {
		h.logger.Error("failed to list categories", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list categories")
		return
	}

	resp := make([]CategoryResponse, len(categories))
	for i, c := range categories {
		resp[i] = toResponse(c)
	}

	writeJSON(w, http.StatusOK, resp)
}

// UpdateCategoryHandler handles PUT /categories/{id}. It updates a
// category's Name, Color, and Icon only — Type is fixed at creation
// (see UpdateCategoryRequest.Validate). Scoping the WHERE clause by
// user_id (not just id) is what blocks editing a global system category
// or another user's category: neither can ever match user_id = ?, so
// the update simply affects zero rows rather than needing a separate
// "is this a system category" check.
func (h *Handler) UpdateCategoryHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	categoryID, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid category id")
		return
	}

	var req UpdateCategoryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "request body is not valid JSON")
		return
	}

	if err := req.Validate(); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	result := h.db.WithContext(r.Context()).
		Model(&Category{}).
		Where("id = ? AND user_id = ?", categoryID, userID).
		Updates(map[string]any{"name": req.Name, "color": req.Color, "icon": req.Icon})
	if result.Error != nil {
		h.logger.Error("failed to update category", "error", result.Error)
		writeError(w, http.StatusInternalServerError, "failed to update category")
		return
	}
	if result.RowsAffected == 0 {
		// Covers "doesn't exist", "belongs to someone else", and "is a
		// global system category" with the same response, so the error
		// never reveals which case it was.
		writeError(w, http.StatusNotFound, "category not found")
		return
	}

	var updated Category
	if err := h.db.WithContext(r.Context()).First(&updated, categoryID).Error; err != nil {
		h.logger.Error("failed to reload category after update", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to update category")
		return
	}

	writeJSON(w, http.StatusOK, toResponse(updated))
}

// DeleteCategoryHandler handles DELETE /categories/{id}. Only a category
// the caller owns can be deleted (same ownership scoping as
// UpdateCategoryHandler — a system category or someone else's category
// looks identically "not found"), and only if no transaction currently
// references it, so deleting a category can never leave existing ledger
// entries pointing at a category_id that no longer exists.
func (h *Handler) DeleteCategoryHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	categoryID, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid category id")
		return
	}

	var existing Category
	if err := h.db.WithContext(r.Context()).
		Where("id = ? AND user_id = ?", categoryID, userID).
		First(&existing).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			writeError(w, http.StatusNotFound, "category not found")
			return
		}
		h.logger.Error("failed to load category for deletion", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to delete category")
		return
	}

	// Queried by raw table name rather than importing internal/transaction
	// directly: that package already imports internal/category (for the
	// Transaction.Category foreign key association), so importing it back
	// here would create an import cycle. A COUNT by column name doesn't
	// need the Go struct type.
	var transactionCount int64
	if err := h.db.WithContext(r.Context()).
		Table("transactions").
		Where("category_id = ?", categoryID).
		Count(&transactionCount).Error; err != nil {
		h.logger.Error("failed to check category usage", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to delete category")
		return
	}
	if transactionCount > 0 {
		writeError(w, http.StatusConflict, "category has existing transactions and cannot be deleted")
		return
	}

	if err := h.db.WithContext(r.Context()).Delete(&existing).Error; err != nil {
		h.logger.Error("failed to delete category", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to delete category")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"message": "category deleted"})
}

// parseIDParam extracts and parses the {id} path value net/http's
// ServeMux populates for patterns like "PUT /categories/{id}".
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
