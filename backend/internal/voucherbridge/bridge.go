// Package voucherbridge connects this Go-owned order database to an independently
// owned voucher service over signed HTTP. It never opens the voucher database.
package voucherbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/dujiao-next/internal/upstream"
	"gorm.io/gorm"
	"net/http"
	"os"
	"strings"
	"time"
)

func Enabled() bool { return os.Getenv("VOUCHER_SERVICE_URL") != "" }
func VoidOrders(refs []string) error {
	if !Enabled() {
		return nil
	}
	payload, _ := json.Marshal(map[string]any{"external_refs": refs})
	path := "/internal/void-many"
	stamp := time.Now().Unix()
	req, err := http.NewRequestWithContext(context.Background(), "POST", strings.TrimRight(os.Getenv("VOUCHER_SERVICE_URL"), "/")+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(upstream.HeaderApiKey, os.Getenv("VOUCHER_API_KEY"))
	req.Header.Set(upstream.HeaderTimestamp, fmt.Sprint(stamp))
	req.Header.Set(upstream.HeaderSignature, upstream.Sign(os.Getenv("VOUCHER_API_SECRET"), "POST", path, stamp, payload))
	client := http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("voucher revocation unavailable; refund blocked")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return errors.New("voucher already redeeming or revocation failed; refund blocked")
	}
	return nil
}

type Procurement interface {
	CreateForOrder(uint) error
	SubmitToUpstream(uint) error
	PollUpstreamStatus(uint) error
}
type Recovery struct {
	DB          *gorm.DB
	Procurement Procurement
	done        chan struct{}
}

func (r *Recovery) Name() string { return "voucher-delivery-recovery" }
func (r *Recovery) Stop(ctx context.Context) error {
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (r *Recovery) Start(ctx context.Context) error {
	r.done = make(chan struct{})
	defer close(r.done)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		r.RunOnce()
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
func (r *Recovery) RunOnce() {
	// The committed paid order itself is the durable intent. Scanning covers the
	// crash window between payment commit and queue insertion, including Redis loss.
	var ids []uint
	r.DB.Table("orders o").Select("DISTINCT o.id").Joins("JOIN order_items i ON i.order_id=o.id").Where("o.paid_at IS NOT NULL AND o.status IN ? AND i.fulfillment_type = ?", []string{"paid", "fulfilling"}, "upstream").Limit(100).Pluck("o.id", &ids)
	for _, id := range ids {
		_ = r.Procurement.CreateForOrder(id)
	}
	var jobs []struct {
		ID     uint
		Status string
	}
	r.DB.Table("procurement_orders p").Select("p.id,p.status").Joins("JOIN orders o ON o.id=p.local_order_id").Where("o.paid_at IS NOT NULL AND o.status IN ? AND p.status IN ?", []string{"paid", "fulfilling"}, []string{"pending", "failed", "accepted"}).Where("p.next_retry_at IS NULL OR p.next_retry_at <= ?", time.Now()).Limit(100).Scan(&jobs)
	for _, j := range jobs {
		if j.Status == "accepted" {
			_ = r.Procurement.PollUpstreamStatus(j.ID)
		} else {
			_ = r.Procurement.SubmitToUpstream(j.ID)
		}
	}
}

func OrderStates(refs []string) (json.RawMessage, error) {
	if !Enabled() {
		return nil, errors.New("voucher service not configured")
	}
	payload, _ := json.Marshal(map[string]any{"external_refs": refs})
	path := "/internal/order-states"
	stamp := time.Now().Unix()
	req, _ := http.NewRequest("POST", strings.TrimRight(os.Getenv("VOUCHER_SERVICE_URL"), "/")+path, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(upstream.HeaderApiKey, os.Getenv("VOUCHER_API_KEY"))
	req.Header.Set(upstream.HeaderTimestamp, fmt.Sprint(stamp))
	req.Header.Set(upstream.HeaderSignature, upstream.Sign(os.Getenv("VOUCHER_API_SECRET"), "POST", path, stamp, payload))
	client := http.Client{Timeout: 10 * time.Second}
	resp, e := client.Do(req)
	if e != nil {
		return nil, e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, errors.New("voucher status unavailable")
	}
	var data json.RawMessage
	e = json.NewDecoder(resp.Body).Decode(&data)
	return data, e
}

// ProtectLedger makes posted wallet transactions append-only at the DB layer.
func ProtectLedger(db *gorm.DB) error {
	if db.Dialector.Name() == "sqlite" {
		for _, op := range []string{"UPDATE", "DELETE"} {
			if e := db.Exec(fmt.Sprintf("CREATE TRIGGER IF NOT EXISTS wallet_immutable_%s BEFORE %s ON wallet_transactions BEGIN SELECT RAISE(ABORT,'wallet ledger is immutable'); END", strings.ToLower(op), op)).Error; e != nil {
				return e
			}
		}
		return nil
	}
	if db.Dialector.Name() == "postgres" {
		return db.Exec(`CREATE OR REPLACE FUNCTION wallet_immutable() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'wallet ledger is immutable'; END $$; DROP TRIGGER IF EXISTS wallet_immutable ON wallet_transactions; CREATE TRIGGER wallet_immutable BEFORE UPDATE OR DELETE ON wallet_transactions FOR EACH ROW EXECUTE FUNCTION wallet_immutable();`).Error
	}
	return errors.New("unsupported ledger database")
}
