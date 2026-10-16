package siteconnection_test

import (
	"errors"
	"github.com/dujiao-next/internal/factorysupply"
	siteconnectionapp "github.com/dujiao-next/internal/modules/siteconnection/application"
	"os"
	"path/filepath"
	"testing"
)

func TestFactoryMainConnectionCannotBeChangedOrRepointed(t *testing.T) {
	file := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(file, []byte(`{"schema_version":1,"shop_id":"shop_test","main_origin":"https://coco9.cc","main_connection_id":7,"reserved_brands":["ChatGPT"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FACTORY_SHOP_ID", "shop_test")
	t.Setenv("FACTORY_SUPPLY_POLICY_FILE", file)
	repo := &siteConnectionRepoStub{conn: newReapplyTestConn()}
	svc := siteconnectionapp.NewService(repo, "test-key", t.TempDir())
	if _, err := svc.Update(7, siteconnectionapp.UpdateInput{BaseURL: "https://else.example"}); !errors.Is(err, factorysupply.ErrManagedSupply) {
		t.Fatal(err)
	}
	if err := svc.Delete(7); !errors.Is(err, factorysupply.ErrManagedSupply) {
		t.Fatal(err)
	}
	if err := svc.SetStatus(7, "disabled"); !errors.Is(err, factorysupply.ErrManagedSupply) {
		t.Fatal(err)
	}
	if _, err := svc.GetAdapter(repo.conn); !errors.Is(err, factorysupply.ErrManagedSupply) {
		t.Fatal(err)
	}
	if _, err := svc.Ping(7); !errors.Is(err, factorysupply.ErrManagedSupply) {
		t.Fatal(err)
	}
	if repo.updated {
		t.Fatal("managed connection changed")
	}
	repo.conn.ID = 8
	if _, err := svc.Update(8, siteconnectionapp.UpdateInput{Name: "other brand supplier"}); err != nil {
		t.Fatal(err)
	}
}
