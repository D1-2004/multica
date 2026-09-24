package dshprofile

import (
	"encoding/json"
	"errors"
)

func ValidateRowOverrides(rows []RowOverride, owned []string) error {
	if len(rows) > 4096 {
		return errors.New("too many plugin configuration rows")
	}
	allowed := map[string]bool{}
	for _, id := range owned {
		allowed[id] = true
	}
	seen := map[string]bool{}
	for _, row := range rows {
		if !rowPattern.MatchString(row.ID) || seen[row.ID] || (owned != nil && !allowed[row.ID]) {
			return errors.New("plugin configuration names an undeclared or duplicate row")
		}
		seen[row.ID] = true
		if len(row.Config) > 60000 || (len(row.Config) > 0 && !json.Valid(row.Config)) {
			return errors.New("invalid plugin row configuration")
		}
	}
	return nil
}
