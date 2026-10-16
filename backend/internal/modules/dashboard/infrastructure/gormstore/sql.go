package gormstore

import (
	"fmt"
	"strings"
	"time"

	"github.com/dujiao-next/internal/constants"

	"gorm.io/gorm"
)

func dialectName(db *gorm.DB) string {
	if db == nil || db.Dialector == nil {
		return "sqlite"
	}
	name := strings.ToLower(strings.TrimSpace(db.Dialector.Name()))
	if name == "" {
		return "sqlite"
	}
	return name
}

func localizedJSONCoalesceExpr(db *gorm.DB, column string) string {
	parts := make([]string, 0, len(constants.SupportedLocales)+1)
	for _, key := range constants.SupportedLocales {
		switch dialectName(db) {
		case "postgres", "postgresql":
			parts = append(parts, fmt.Sprintf("(%s::jsonb ->> '%s')", column, key))
		default:
			parts = append(parts, fmt.Sprintf("json_extract(%s, '$.\"%s\"')", column, key))
		}
	}
	parts = append(parts, "''")
	return fmt.Sprintf("COALESCE(%s)", strings.Join(parts, ", "))
}

func dateGroupExpr(db *gorm.DB, column string, loc *time.Location, refTime time.Time) string {
	if loc == nil {
		loc = time.UTC
	}
	switch dialectName(db) {
	case "postgres", "postgresql":
		zoneName := loc.String()
		if zoneName == "" || zoneName == "Local" {
			zoneName = "UTC"
		}
		return fmt.Sprintf("TO_CHAR(%s AT TIME ZONE '%s', 'YYYY-MM-DD')", column, zoneName)
	default:
		_, offset := refTime.In(loc).Zone()
		sign := "+"
		if offset < 0 {
			sign = "-"
			offset = -offset
		}
		hours := offset / 3600
		minutes := (offset % 3600) / 60
		if minutes != 0 {
			return fmt.Sprintf("strftime('%%Y-%%m-%%d', %s, '%s%d hours', '%s%d minutes')", column, sign, hours, sign, minutes)
		}
		return fmt.Sprintf("strftime('%%Y-%%m-%%d', %s, '%s%d hours')", column, sign, hours)
	}
}

// quotedStatusList only accepts internal status constants, never user input.
func quotedStatusList(statuses []string) string {
	parts := make([]string, len(statuses))
	for index, status := range statuses {
		parts[index] = "'" + status + "'"
	}
	return strings.Join(parts, ",")
}
