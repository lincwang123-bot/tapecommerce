package domain

import "time"

// ChainIntent is an immutable invoice plus mutable verification progress. It
// deliberately has no soft-delete field: its exact amount can never be reused.
type ChainIntent struct {
	PaymentID                                                   uint `gorm:"primaryKey;autoIncrement:false"`
	OrderID, UserID                                             uint
	Network                                                     string `gorm:"size:24;not null;uniqueIndex:chain_amount_reservation"`
	TokenContract                                               string `gorm:"size:80;not null;uniqueIndex:chain_amount_reservation"`
	Recipient                                                   string `gorm:"size:80;not null;uniqueIndex:chain_amount_reservation"`
	ExpectedUnits                                               string `gorm:"size:78;not null;uniqueIndex:chain_amount_reservation"`
	ChainID                                                     uint64
	Decimals                                                    uint8
	ChainLabel, ChainAmount, AmountAdjustment, SettlementAmount string
	MinBlockNumber                                              uint64
	CreatedAt, ExpiresAt                                        time.Time
	SubmittedHash                                               string     `gorm:"size:80"`
	VerificationStatus                                          string     `gorm:"size:32;index"`
	VerificationCode                                            string     `gorm:"size:64"`
	ReceivedUnits                                               string     `gorm:"size:78"`
	NextCheckAt                                                 *time.Time `gorm:"index"`
	Attempts                                                    uint
	LeaseToken                                                  string     `gorm:"size:64"`
	LeaseUntil                                                  *time.Time `gorm:"index"`
	DiscoveryCursor                                             string     `gorm:"type:text"`
	DiscoveryCode                                               string     `gorm:"size:64"`
	DiscoveryNextAt                                             *time.Time `gorm:"index"`
	DiscoveryLease                                              string     `gorm:"size:64"`
	DiscoveryLeaseUntil                                         *time.Time
	DiscoveryUpdatedAt                                          *time.Time
	UpdatedAt                                                   time.Time
}

func (ChainIntent) TableName() string { return "chain_payment_intents" }

type ChainReceipt struct {
	ID                                            uint   `gorm:"primaryKey"`
	Network                                       string `gorm:"size:24;not null;uniqueIndex:chain_event_claim"`
	TxHash                                        string `gorm:"size:80;not null;uniqueIndex:chain_event_claim"`
	LogIndex                                      uint64 `gorm:"not null;uniqueIndex:chain_event_claim"`
	PaymentID                                     uint   `gorm:"not null;uniqueIndex"`
	TokenContract, Recipient, Sender, AmountUnits string
	BlockNumber, FinalizedBlockNumber             uint64
	BlockHash                                     string
	PaidAt, CreatedAt                             time.Time
}

func (ChainReceipt) TableName() string { return "chain_payment_receipts" }
