package application

import (
	"github.com/dujiao-next/internal/constants"
	cardsecretdomain "github.com/dujiao-next/internal/modules/cardsecret/domain"
	fulfillmentdomain "github.com/dujiao-next/internal/modules/fulfillment/domain"
	orderapp "github.com/dujiao-next/internal/modules/order/application"
	ordercontract "github.com/dujiao-next/internal/modules/order/contract"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	"strings"
	"time"
)

// CreateAutoInTransaction reuses the original stock allocation and delivery
// logic inside a caller-owned transaction. It does not commit, send notifications
// or acquire another connection. Call NotifyAutoCommitted after successful commit.
func CreateAutoInTransaction(tx ordercontract.Transaction, orderID uint, now time.Time) (fulfillment *fulfillmentdomain.Fulfillment, order *orderdomain.Order, parentStatus string, err error) {
	if tx == nil || orderID == 0 {
		return nil, nil, "", ErrFulfillmentInvalid
	}
	err = func() error {
		order, err = tx.Orders().GetByID(orderID)
		if err != nil {
			return err
		}
		if order == nil {
			return ErrOrderNotFound
		}
		// Share cancellation/refund lock order: parent, then child.
		if order.ParentID != nil {
			if _, err := tx.Orders().GetByIDForUpdate(*order.ParentID); err != nil {
				return err
			}
		}
		// Serialize with cancellation/refunds, then recheck the committed state.
		locked, err := tx.Orders().GetByIDForUpdateWithChildren(orderID)
		if err != nil {
			return err
		}
		if locked == nil {
			return ErrOrderNotFound
		}
		if locked.Status != constants.OrderStatusPaid {
			return ErrOrderStatusInvalid
		}
		order = locked
		if (order.ParentID == nil && len(order.Children) > 0) || len(order.Items) == 0 {
			return ErrFulfillmentInvalid
		}
		for _, item := range order.Items {
			if strings.TrimSpace(item.FulfillmentType) != constants.FulfillmentTypeAuto {
				return ErrFulfillmentNotAuto
			}
		}

		if _, found, err := tx.Fulfillments().FindByOrderIDForUpdate(orderID); err != nil {
			return err
		} else if found {
			return ErrFulfillmentExists
		}

		secretRepo := tx.CardSecrets()
		reservedRows, err := secretRepo.ListByOrderAndStatus(orderID, cardsecretdomain.StatusReserved)
		if err != nil {
			return err
		}
		reservedByKey := make(map[string][]cardsecretdomain.Secret)
		for _, reserved := range reservedRows {
			key := orderdomain.ItemKey(reserved.ProductID, reserved.SKUID)
			reservedByKey[key] = append(reservedByKey[key], reserved)
		}
		var secrets []cardsecretdomain.Secret
		for _, item := range order.Items {
			if item.ProductID == 0 || item.Quantity <= 0 {
				return ErrFulfillmentInvalid
			}
			key := orderdomain.ItemKey(item.ProductID, item.SKUID)
			cachedReserved := reservedByKey[key]
			selected := make([]cardsecretdomain.Secret, 0, item.Quantity)
			if len(cachedReserved) > 0 {
				take := item.Quantity
				if len(cachedReserved) < take {
					take = len(cachedReserved)
				}
				selected = append(selected, cachedReserved[:take]...)
				reservedByKey[key] = cachedReserved[take:]
			}

			if len(selected) < item.Quantity {
				need := item.Quantity - len(selected)
				availableRows, err := secretRepo.ListAvailableByProduct(item.ProductID, item.SKUID, need)
				if err != nil {
					return err
				}
				selected = append(selected, availableRows...)
			}
			if len(selected) < item.Quantity {
				return ErrCardSecretInsufficient
			}
			secrets = append(secrets, selected...)
		}

		ids := make([]uint, 0, len(secrets))
		secretLines := make([]string, 0, len(secrets))
		for _, secret := range secrets {
			ids = append(ids, secret.ID)
			secretLines = append(secretLines, secret.Secret)
		}

		affected, err := secretRepo.MarkUsed(ids, orderID, now)
		if err != nil {
			return err
		}
		if int(affected) != len(ids) {
			return ErrCardSecretInsufficient
		}

		separator := "\n"
		for _, value := range secretLines {
			if strings.ContainsAny(value, "\r\n") {
				separator = "\n\n"
				break
			}
		}
		payload := strings.Join(secretLines, separator)
		fulfillment = &fulfillmentdomain.Fulfillment{
			OrderID:     orderID,
			Type:        constants.FulfillmentTypeAuto,
			Status:      constants.FulfillmentStatusDelivered,
			Payload:     payload,
			DeliveredAt: &now,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if err := tx.Fulfillments().Create(fulfillment); err != nil {
			return ErrFulfillmentCreateFailed
		}
		if err := tx.Orders().UpdateFields(orderID, map[string]interface{}{
			"status":     constants.OrderStatusCompleted,
			"updated_at": now,
		}); err != nil {
			return ErrOrderUpdateFailed
		}
		if order.ParentID != nil {
			var err error
			parentStatus, err = orderapp.SyncParentStatus(tx.Orders(), *order.ParentID, now)
			if err != nil {
				return err
			}
		}
		return nil
	}()
	return
}
