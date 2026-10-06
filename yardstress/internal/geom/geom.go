// Package geom provides the small axis-aligned rectangle type shared by the
// yard model (stack footprints), the allowable-stress zones and the
// discretizer.
package geom

// Rect is an axis-aligned rectangle in the yard plane, metres.
type Rect struct {
	MinX, MinY, MaxX, MaxY float64
}

// Valid reports whether the rectangle has strictly positive side lengths.
func (r Rect) Valid() bool { return r.MinX < r.MaxX && r.MinY < r.MaxY }

func (r Rect) Width() float64  { return r.MaxX - r.MinX }
func (r Rect) Height() float64 { return r.MaxY - r.MinY }
func (r Rect) Area() float64   { return r.Width() * r.Height() }

// Center returns the rectangle centroid.
func (r Rect) Center() (float64, float64) {
	return (r.MinX + r.MaxX) / 2, (r.MinY + r.MaxY) / 2
}

// Overlaps reports whether two rectangles share a positive-area region.
// Touching edges do not count as overlap.
func (r Rect) Overlaps(o Rect) bool {
	return r.MinX < o.MaxX && o.MinX < r.MaxX && r.MinY < o.MaxY && o.MinY < r.MaxY
}

// Expand grows the rectangle by d on every side.
func (r Rect) Expand(d float64) Rect {
	return Rect{MinX: r.MinX - d, MinY: r.MinY - d, MaxX: r.MaxX + d, MaxY: r.MaxY + d}
}

// Contains reports whether (x, y) lies inside the rectangle, edges included.
func (r Rect) Contains(x, y float64) bool {
	return x >= r.MinX && x <= r.MaxX && y >= r.MinY && y <= r.MaxY
}
