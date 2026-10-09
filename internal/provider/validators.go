package provider

import (
	"context"
	"strconv"
	"strings"
	"unicode"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

// Nanelo silently rewrites some inputs (lowercases names, strips trailing dots, trims values).
// A rewritten value no longer matches the configuration, which Terraform reports as an
// inconsistent result after apply, so these validators reject such inputs up front.

func zoneName() validator.String {
	return stringCheck{"must be a lowercase domain name without a trailing dot", func(s string) string {
		if s == "" || s != strings.ToLower(s) || strings.HasSuffix(s, ".") || strings.ContainsFunc(s, unicode.IsSpace) {
			return "got " + strconv.Quote(s)
		}
		return ""
	}}
}

func recordName() validator.String {
	return stringCheck{"must be a lowercase, fully-qualified name without a trailing dot", func(s string) string {
		switch {
		case s == "" || strings.ContainsFunc(s, unicode.IsSpace):
			return "got " + strconv.Quote(s)
		case s != strings.ToLower(s):
			return "Nanelo stores names in lowercase; use " + strconv.Quote(strings.ToLower(s))
		case strings.HasSuffix(s, "."):
			return "Nanelo strips trailing dots from names; use " + strconv.Quote(strings.TrimSuffix(s, "."))
		}
		return ""
	}}
}

func recordValue() validator.String {
	return stringCheck{"must be non-empty, without surrounding whitespace or characters outside the Basic Multilingual Plane", func(s string) string {
		switch {
		case s == "":
			return "got an empty value"
		case strings.TrimSpace(s) != s:
			return "Nanelo trims surrounding whitespace from values"
		case strings.ContainsFunc(s, func(r rune) bool { return r > 0xFFFF }):
			return "Nanelo cannot store characters such as emoji"
		}
		return ""
	}}
}

// stringCheck is a validator whose check returns a problem description, or "" if valid.
type stringCheck struct {
	desc  string
	check func(string) string
}

func (v stringCheck) Description(context.Context) string         { return v.desc }
func (v stringCheck) MarkdownDescription(context.Context) string { return v.desc }

func (v stringCheck) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if problem := v.check(req.ConfigValue.ValueString()); problem != "" {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid value", "Value "+v.desc+": "+problem+".")
	}
}
