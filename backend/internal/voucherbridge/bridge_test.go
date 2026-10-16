package voucherbridge

import (
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"testing"
)

func TestImmutableLedger(t *testing.T) {
	db, e := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if e != nil {
		t.Fatal(e)
	}
	db.Exec("CREATE TABLE wallet_transactions(id INTEGER PRIMARY KEY,amount INTEGER)")
	if e := ProtectLedger(db); e != nil {
		t.Fatal(e)
	}
	db.Exec("INSERT INTO wallet_transactions VALUES(1,20)")
	if db.Exec("UPDATE wallet_transactions SET amount=30 WHERE id=1").Error == nil {
		t.Fatal("ledger mutable")
	}
	if db.Exec("DELETE FROM wallet_transactions WHERE id=1").Error == nil {
		t.Fatal("ledger deletable")
	}
}
