package emailverificationdomain

import "time"

// SendReservation bounds concurrent mail delivery before calling an external provider.
type SendReservation struct {
	Key           string    `gorm:"primaryKey;size:64"`
	NextAllowedAt time.Time `gorm:"not null;index"`
}

func (SendReservation) TableName() string { return "email_send_reservations" }

// SendBudget is a bounded single-row global delivery budget.
type SendBudget struct {
	Key         string    `gorm:"primaryKey;size:32"`
	WindowStart time.Time `gorm:"not null"`
	Count       int       `gorm:"not null"`
}

func (SendBudget) TableName() string { return "email_send_budgets" }
