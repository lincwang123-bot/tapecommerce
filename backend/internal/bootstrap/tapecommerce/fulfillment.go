// Package tapecommercebootstrap defines opt-in bridges to the existing shop.
// Live composition is explicitly configured and disabled by default.
package tapecommercebootstrap

import (
	"context"
	"errors"

	"github.com/dujiao-next/internal/constants"
	fulfillmentapp "github.com/dujiao-next/internal/modules/fulfillment/application"
	fulfillmentdomain "github.com/dujiao-next/internal/modules/fulfillment/domain"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	"github.com/dujiao-next/internal/modules/tapecommerce/domain"
)

type OrderReader interface {
	GetByID(uint) (*orderdomain.Order, error)
}
type DeliveryReader interface {
	GetByOrderID(uint) (*fulfillmentdomain.Fulfillment, error)
}
type AutoDelivery interface {
	CreateAuto(uint) (*fulfillmentdomain.Fulfillment, error)
}

// StockFulfiller only handles an existing paid stock order in this isolated
// shop instance. It never marks an order paid or submits an upstream purchase.
// The audited payment/order bridge must run before this port is enabled.
// CreateAuto already locks the order and has a unique fulfillment order_id.
// This is NOT contract.AtomicAdmission: its transaction does not lock the
// TapeCommerce invoice/outbox and it cannot be wired into Service.DeliverOne.
type StockFulfiller struct {
	ShopID     string
	Orders     OrderReader
	Deliveries DeliveryReader
	Auto       AutoDelivery
}

func (f StockFulfiller) Fulfill(ctx context.Context, job domain.Job) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.ShopID == "" || job.ShopID != f.ShopID || job.OrderID == 0 || job.Key != "tapecommerce:"+job.InvoiceID || !domain.ValidHex(job.InvoiceID, 32) {
		return domain.ErrReview
	}
	if f.Orders == nil || f.Deliveries == nil || f.Auto == nil {
		return domain.ErrDisabled
	}
	order, err := f.Orders.GetByID(job.OrderID)
	if err != nil {
		return err
	}
	if order == nil || order.ResellerID != nil || len(order.Children) > 0 || len(order.Items) == 0 {
		return domain.ErrReview
	}
	for _, item := range order.Items {
		if item.FulfillmentType != constants.FulfillmentTypeAuto || item.SupplySource == "main" {
			return domain.ErrReview
		}
	}
	if order.Status == constants.OrderStatusCompleted || order.Status == constants.OrderStatusDelivered {
		return f.confirmExisting(job.OrderID)
	}
	if order.Status != constants.OrderStatusPaid {
		return domain.ErrReview
	}
	_, err = f.Auto.CreateAuto(job.OrderID)
	if err == nil {
		return nil
	}
	// A concurrent attempt/crash may have committed the existing fulfillment.
	if errors.Is(err, fulfillmentapp.ErrFulfillmentExists) || errors.Is(err, fulfillmentapp.ErrOrderStatusInvalid) {
		if existingErr := f.confirmExisting(job.OrderID); existingErr == nil {
			return nil
		}
	}
	if errors.Is(err, fulfillmentapp.ErrCardSecretInsufficient) || errors.Is(err, fulfillmentapp.ErrFulfillmentNotAuto) || errors.Is(err, fulfillmentapp.ErrOrderStatusInvalid) {
		return domain.ErrReview
	}
	return err
}

func (f StockFulfiller) confirmExisting(id uint) error {
	order, err := f.Orders.GetByID(id)
	if err != nil {
		return err
	}
	if order == nil || (order.Status != constants.OrderStatusCompleted && order.Status != constants.OrderStatusDelivered) {
		return domain.ErrReview
	}
	delivery, err := f.Deliveries.GetByOrderID(id)
	if err != nil {
		return err
	}
	if delivery == nil || delivery.Status != constants.FulfillmentStatusDelivered || delivery.Type != constants.FulfillmentTypeAuto {
		return domain.ErrReview
	}
	return nil
}
