package category

// colorHex resolves every token in validColors to its Tailwind 500-step
// hex value — the same shade the frontend renders for a category's icon
// badge (see lib/category-ui.tsx's CATEGORY_COLOR_BG), so a chart segment
// and that category's badge always read as the same color.
var colorHex = map[string]string{
	"red":     "#ef4444",
	"orange":  "#f97316",
	"amber":   "#f59e0b",
	"yellow":  "#eab308",
	"lime":    "#84cc16",
	"green":   "#22c55e",
	"emerald": "#10b981",
	"teal":    "#14b8a6",
	"cyan":    "#06b6d4",
	"sky":     "#0ea5e9",
	"blue":    "#3b82f6",
	"indigo":  "#6366f1",
	"violet":  "#8b5cf6",
	"purple":  "#a855f7",
	"fuchsia": "#d946ef",
	"pink":    "#ec4899",
	"rose":    "#f43f5e",
}

// fallbackColorHex is used for a color token this package doesn't
// recognize (data predating a palette change, or simply absent — e.g.
// an "Uncategorized" bucket has no token at all). zinc-500, a neutral
// that reads as "no category" rather than impersonating a real one.
const fallbackColorHex = "#71717a"

// ColorHex resolves a stored color token to the hex value clients
// render it as (chart fills, in particular — an SVG fill attribute
// needs a real color, not a Tailwind class name). Never errors: an
// unrecognized token falls back to a neutral gray so a chart can't
// break over bad or missing data.
func ColorHex(token string) string {
	if hex, ok := colorHex[token]; ok {
		return hex
	}
	return fallbackColorHex
}
