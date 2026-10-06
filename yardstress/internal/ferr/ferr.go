// Package ferr defines field-level validation errors shared by the
// configuration loader, the yard model and the admission engine. The HTTP
// layer renders them verbatim so callers can see exactly which field was
// rejected and why.
package ferr

import (
	"fmt"
	"strings"
)

// FieldError pinpoints a single invalid field.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (e FieldError) Error() string { return fmt.Sprintf("%s: %s", e.Field, e.Message) }

// List is a collection of field errors.
type List []FieldError

func (l List) Error() string {
	parts := make([]string, len(l))
	for i, e := range l {
		parts[i] = e.Error()
	}
	return strings.Join(parts, "; ")
}

// Has reports whether an error for the given field path is present.
func (l List) Has(field string) bool {
	for _, e := range l {
		if e.Field == field {
			return true
		}
	}
	return false
}

// Appendf adds a formatted error for field.
func Appendf(l List, field, format string, args ...any) List {
	return append(l, FieldError{Field: field, Message: fmt.Sprintf(format, args...)})
}
