package handler

import (
	"regexp"
	"strings"
)

// Presentation helpers for model-visible Host data. Raw Task, Run, plan and
// queue UUIDs are Host locators: the model cannot use them (its tools take
// source-bound refs) and they leaked into replies. Host-private fields keep
// the exact IDs.

var employeeUUIDPattern = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

// employeeModelRefKind keeps only the kind of a Host reference such as
// "plan:<uuid>" or "run:<uuid>"; a bare UUID becomes "ref".
func employeeModelRefKind(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	kind, _, found := strings.Cut(ref, ":")
	if !found {
		if employeeUUIDPattern.MatchString(ref) {
			return "ref"
		}
		return employeeCatalogLabel(ref, 64)
	}
	return employeeCatalogLabel(employeeUUIDPattern.ReplaceAllString(kind, "id"), 64)
}

// employeeModelScrubIDs replaces every UUID inside a Host string with "id"
// and keeps the rest, for example "member:id".
func employeeModelScrubIDs(text string) string {
	return employeeUUIDPattern.ReplaceAllString(text, "id")
}
