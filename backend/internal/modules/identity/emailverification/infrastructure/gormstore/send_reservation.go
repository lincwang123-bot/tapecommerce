package emailverificationstore

import (
	"crypto/sha256"
	"errors"
	"fmt"
	emailverificationdomain "github.com/dujiao-next/internal/modules/identity/emailverification/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

func (store *Store) ReserveSend(email, purpose string, now time.Time, interval time.Duration) (bool, error) {
	// Reserve both budgets atomically only after address and CAPTCHA validation.
	limited := errors.New("email send budget exhausted")
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(email)))
	err := store.db.Transaction(func(tx *gorm.DB) error {
		reservation := emailverificationdomain.SendReservation{Key: key, NextAllowedAt: now.Add(interval)}
		result := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "key"}},
			DoUpdates: clause.AssignmentColumns([]string{"next_allowed_at"}),
			Where:     clause.Where{Exprs: []clause.Expression{clause.Lte{Column: clause.Column{Table: "email_send_reservations", Name: "next_allowed_at"}, Value: now}}},
		}).Create(&reservation)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return limited
		}
		window := now.UTC().Truncate(time.Hour)
		budget := emailverificationdomain.SendBudget{Key: "site", WindowStart: window, Count: 1}
		result = tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "key"}},
			DoUpdates: clause.Assignments(map[string]interface{}{
				"window_start": window,
				"count":        gorm.Expr("CASE WHEN email_send_budgets.window_start < ? THEN 1 ELSE email_send_budgets.count + 1 END", window),
			}),
			Where: clause.Where{Exprs: []clause.Expression{clause.Or(clause.Lt{Column: clause.Column{Table: "email_send_budgets", Name: "window_start"}, Value: window}, clause.Lt{Column: clause.Column{Table: "email_send_budgets", Name: "count"}, Value: 100})}},
		}).Create(&budget)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return limited
		}
		return nil
	})
	if errors.Is(err, limited) {
		return false, nil
	}
	return err == nil, err
}
