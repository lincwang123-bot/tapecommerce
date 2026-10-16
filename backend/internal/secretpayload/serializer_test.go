package secretpayload

import (
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"strings"
	"testing"
)

func (row) TableName() string { return "fulfillments" }

type row struct {
	ID      uint
	Payload string `gorm:"serializer:delivery_secret"`
}

func TestPersistedEncryption(t *testing.T) {
	t.Setenv("VOUCHER_DELIVERY_KEY", strings.Repeat("k", 32))
	db, e := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if e != nil {
		t.Fatal(e)
	}
	Register(db)
	db.AutoMigrate(&row{})
	r := row{Payload: "SC-secret"}
	if e := db.Create(&r).Error; e != nil {
		t.Fatal(e)
	}
	var raw string
	db.Raw("SELECT payload FROM fulfillments WHERE id=?", r.ID).Scan(&raw)
	if strings.Contains(raw, "SC-secret") {
		t.Fatal("plaintext persisted")
	}
	var got row
	db.First(&got, r.ID)
	if got.Payload != "SC-secret" {
		t.Fatal(got.Payload)
	}
	if e := db.Model(&row{}).Where("id=?", r.ID).Updates(map[string]any{"payload": "SC-next"}).Error; e != nil {
		t.Fatal(e)
	}
	db.Raw("SELECT payload FROM fulfillments WHERE id=?", r.ID).Scan(&raw)
	if strings.Contains(raw, "SC-next") {
		t.Fatal("map update persisted plaintext")
	}
}
