package selfhost_test

import (
	"strings"
	"testing"

	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	procurementdomain "github.com/dujiao-next/internal/modules/procurement/domain"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"github.com/dujiao-next/internal/voucherbridge"
)

type recoveryProcurement struct {
	created           []uint
	submitted, polled int
}

func (p *recoveryProcurement) CreateForOrder(id uint) error {
	p.created = append(p.created, id)
	return nil
}
func (p *recoveryProcurement) SubmitToUpstream(uint) error   { p.submitted++; return nil }
func (p *recoveryProcurement) PollUpstreamStatus(uint) error { p.polled++; return nil }

// Settlement commits without a queue; the paid database order remains the
// recovery intent. This fake boundary never performs supplier HTTP requests.
func TestSelfhostCommittedPaymentRecoversMissingDeliveryEnqueue(t *testing.T) {
	f := setup(t)
	if e := f.db.AutoMigrate(&procurementdomain.Order{}); e != nil {
		t.Fatal(e)
	}
	paid, unpaid := f.order(t), f.order(t)
	for _, id := range []uint{paid.OrderID, unpaid.OrderID} {
		item := orderdomain.OrderItem{OrderID: id, ProductID: 1, SKUID: 1, Quantity: 1, TitleJSON: jsonmap.JSON{"en": "Synthetic recovery fixture"}, FulfillmentType: "upstream"}
		if e := f.db.Create(&item).Error; e != nil {
			t.Fatal(e)
		}
	}
	boundary := &recoveryProcurement{}
	recovery := &voucherbridge.Recovery{DB: f.db, Procurement: boundary}
	recovery.RunOnce()
	if len(boundary.created) != 0 {
		t.Fatal("unpaid order scheduled")
	}
	if _, e := submit(f, paid, "0x"+strings.Repeat("a", 64)); e != nil {
		t.Fatal(e)
	}
	var committed orderdomain.Order
	if e := f.db.First(&committed, paid.OrderID).Error; e != nil {
		t.Fatal(e)
	}
	if committed.PaidAt == nil || committed.Status != "paid" {
		t.Fatal("payment not durably paid", committed.Status)
	}
	var jobs int64
	if e := f.db.Model(&procurementdomain.Order{}).Count(&jobs).Error; e != nil {
		t.Fatal(e)
	}
	if jobs != 0 {
		t.Fatal("fixture unexpectedly enqueued fulfillment")
	}
	recovery.RunOnce()
	if len(boundary.created) != 1 || boundary.created[0] != paid.OrderID {
		t.Fatal("recovery did not isolate paid order", boundary.created)
	}
	if boundary.submitted != 0 || boundary.polled != 0 {
		t.Fatal("unexpected supplier execution")
	}
	// A fresh recovery instance can rediscover the durable intent after restart.
	restarted := &voucherbridge.Recovery{DB: f.db, Procurement: boundary}
	restarted.RunOnce()
	if len(boundary.created) != 2 || boundary.created[1] != paid.OrderID {
		t.Fatal("restart lost paid intent")
	}
}
