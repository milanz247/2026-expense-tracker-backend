package category

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Supported category types. Deliberately just income/expense — a
// transfer moves money between the caller's own wallets rather than
// representing earned or spent money, so it has no category to assign.
const (
	TypeIncome  = "income"
	TypeExpense = "expense"
)

var validCategoryTypes = map[string]bool{
	TypeIncome:  true,
	TypeExpense: true,
}

// validColors is the closed set of Tailwind color tokens the frontend's
// "Color Preset Picker" offers (see lib/categories.ts's
// CATEGORY_COLORS, which must stay in sync with this list). Storing a
// token instead of an arbitrary hex/class string means a category can
// never be pointed at CSS nobody has reviewed.
var validColors = map[string]bool{
	"red": true, "orange": true, "amber": true, "yellow": true, "lime": true,
	"green": true, "emerald": true, "teal": true, "cyan": true, "sky": true,
	"blue": true, "indigo": true, "violet": true, "purple": true, "fuchsia": true,
	"pink": true, "rose": true,
	// The four neutral/grayscale tokens below exist for the
	// auto-assign-only system categories (see system.go and
	// pkg/database/seed.go) — a "Transfer" or "Debt Issued" badge reads
	// as administrative/neutral, not as a real income or expense
	// category, so it deliberately stays off the vivid palette above.
	// Nothing stops a user from picking one for their own category too.
	"slate": true, "zinc": true, "neutral": true, "stone": true,
}

// validIcons is the closed set of Lucide icon names the frontend's
// "Icon selector" offers (see lib/categories.ts's CATEGORY_ICONS, which
// must stay in sync with this list).
var validIcons = map[string]bool{
	"TrendingUp": true, "TrendingDown": true, "Coins": true, "Wallet": true,
	"Utensils": true, "Car": true, "Home": true, "Clapperboard": true,
	"Zap": true, "HeartPulse": true, "ShoppingBag": true, "Gift": true,
	"Plane": true, "GraduationCap": true, "Briefcase": true, "Smartphone": true,
	"Dumbbell": true, "PawPrint": true, "CreditCard": true, "Tag": true,
	"Music": true, "Shirt": true,
	// The four icons below are reserved for the auto-assign-only system
	// categories (see system.go and pkg/database/seed.go).
	"ArrowLeftRight": true, "BookOpen": true, "HandCoins": true, "CheckCircle": true,
}

// CreateCategoryRequest is the expected JSON payload for POST
// /categories.
type CreateCategoryRequest struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Color string `json:"color"`
	Icon  string `json:"icon"`
}

// Validate normalizes and applies basic business rules to the incoming
// payload. It returns a descriptive error suitable for returning
// directly to the client on failure.
func (r *CreateCategoryRequest) Validate() error {
	r.Name = strings.TrimSpace(r.Name)
	r.Type = strings.ToLower(strings.TrimSpace(r.Type))
	r.Color = strings.ToLower(strings.TrimSpace(r.Color))
	r.Icon = strings.TrimSpace(r.Icon)

	if r.Name == "" {
		return errors.New("name is required")
	}
	if len(r.Name) > 100 {
		return errors.New("name must be at most 100 characters")
	}
	if !validCategoryTypes[r.Type] {
		return fmt.Errorf("type must be one of: %s, %s", TypeIncome, TypeExpense)
	}
	if !validColors[r.Color] {
		return errors.New("color must be one of the supported preset colors")
	}
	if !validIcons[r.Icon] {
		return errors.New("icon must be one of the supported preset icons")
	}

	return nil
}

// UpdateCategoryRequest is the expected JSON payload for PUT
// /categories/{id}.
type UpdateCategoryRequest struct {
	Name  string `json:"name"`
	Color string `json:"color"`
	Icon  string `json:"icon"`
}

// Validate applies the same rules as CreateCategoryRequest except Type,
// which is intentionally not editable: flipping a category from expense
// to income (or back) after transactions have used it for reporting
// would silently corrupt historical income/expense totals, so Type can
// only ever be set at creation.
func (r *UpdateCategoryRequest) Validate() error {
	r.Name = strings.TrimSpace(r.Name)
	r.Color = strings.ToLower(strings.TrimSpace(r.Color))
	r.Icon = strings.TrimSpace(r.Icon)

	if r.Name == "" {
		return errors.New("name is required")
	}
	if len(r.Name) > 100 {
		return errors.New("name must be at most 100 characters")
	}
	if !validColors[r.Color] {
		return errors.New("color must be one of the supported preset colors")
	}
	if !validIcons[r.Icon] {
		return errors.New("icon must be one of the supported preset icons")
	}

	return nil
}

// CategoryResponse is the JSON shape returned to clients for a single
// category. IsSystem tells the frontend whether to show Edit/Delete —
// every seeded category (UserID IS NULL) is read-only for every user,
// which the backend also enforces independently: UpdateCategoryHandler
// and DeleteCategoryHandler scope their WHERE clause by user_id, so a
// UserID-nil row can never match regardless of what IsSystem says. This
// field is purely a UI hint, straight from the stored column (see
// model.go's Category.IsSystem) rather than re-derived from UserID.
type CategoryResponse struct {
	ID        uint      `json:"id"`
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	Color     string    `json:"color"`
	Icon      string    `json:"icon"`
	IsSystem  bool      `json:"is_system"`
	CreatedAt time.Time `json:"created_at"`
}

func toResponse(c Category) CategoryResponse {
	return CategoryResponse{
		ID:        c.ID,
		Name:      c.Name,
		Type:      c.Type,
		Color:     c.Color,
		Icon:      c.Icon,
		IsSystem:  c.IsSystem,
		CreatedAt: c.CreatedAt,
	}
}
