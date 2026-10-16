package application

import (
	"fmt"
	"strings"
	"time"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/logger"
	procurementcontract "github.com/dujiao-next/internal/modules/procurement/contract"
)

// HandleUpstreamCallback 处理上游回调通知
func (s *Service) HandleUpstreamCallback(procurementOrderID uint, upstreamStatus string, fulfillment *procurementcontract.Fulfillment) error {
	procOrder, err := s.procRepo.GetByID(procurementOrderID)
	if err != nil {
		return fmt.Errorf("load procurement order: %w", err)
	}
	if procOrder == nil {
		return procurementcontract.ErrNotFound
	}

	now := time.Now()
	upstreamStatus = strings.ToLower(strings.TrimSpace(upstreamStatus))

	switch upstreamStatus {
	case "delivered", "completed", "fulfilled":
		// 更新采购单状态
		updates := map[string]interface{}{
			"updated_at": now,
		}
		if fulfillment != nil {
			updates["upstream_payload"] = fulfillment.Payload
		}
		if err := s.procRepo.UpdateStatus(procOrder.ID, "fulfilled", updates); err != nil {
			return fmt.Errorf("update procurement status: %w", err)
		}

		// 在本地订单上创建交付记录
		if fulfillment != nil && s.orderLifecycle != nil {
			if err := s.createUpstreamFulfillment(procOrder.LocalOrderID, fulfillment, now); err != nil {
				logger.Warnw("procurement_create_fulfillment_failed",
					"procurement_order_id", procOrder.ID,
					"local_order_id", procOrder.LocalOrderID,
					"error", err,
				)
				return err
			}
		}

		// 更新本地订单状态
		_ = s.orderRepo.UpdateStatus(procOrder.LocalOrderID, constants.OrderStatusDelivered, map[string]interface{}{
			"updated_at": now,
		})

		// 如果有父订单，同步父订单状态
		localOrder, _ := s.orderRepo.GetByID(procOrder.LocalOrderID)
		if localOrder != nil && localOrder.ParentID != nil && s.orderLifecycle != nil {
			if status, syncErr := s.orderLifecycle.SyncParentStatus(*localOrder.ParentID, now); syncErr != nil {
				logger.Warnw("procurement_sync_parent_status_failed",
					"procurement_order_id", procOrder.ID,
					"parent_order_id", *localOrder.ParentID,
					"error", syncErr,
				)
			} else {
				if status == "" {
					status = constants.OrderStatusDelivered
				}
				if status != constants.OrderStatusCanceled {
					_, _ = s.orderLifecycle.EnqueueStatusEmail(*localOrder.ParentID, status)
				}
			}
		} else if localOrder != nil && s.orderLifecycle != nil {
			_, _ = s.orderLifecycle.EnqueueStatusEmail(localOrder.ID, constants.OrderStatusDelivered)
		}

		// 触发下游回调（多级连跳：本站作为中间节点，通知下游交付完成）
		if s.downstreamCallback != nil {
			s.downstreamCallback.EnqueueCallback(procOrder.LocalOrderID)
			// 如果有父订单，也通知父订单的下游
			if localOrder != nil && localOrder.ParentID != nil {
				s.downstreamCallback.EnqueueCallback(*localOrder.ParentID)
			}
		}

		// 通知 Bot 订单已交付
		if s.botNotifier != nil && localOrder != nil {
			notifyOrderID := localOrder.ID
			if localOrder.ParentID != nil {
				notifyOrderID = *localOrder.ParentID
			}
			go s.botNotifier.NotifyBotOrderFulfilled(localOrder.UserID, notifyOrderID)
		}

		logger.Infow("procurement_order_fulfilled",
			"procurement_order_id", procOrder.ID,
			"local_order_id", procOrder.LocalOrderID,
		)

	case "canceled":
		updates := map[string]interface{}{
			"updated_at": now,
		}
		if err := s.procRepo.UpdateStatus(procOrder.ID, "canceled", updates); err != nil {
			return fmt.Errorf("update procurement status: %w", err)
		}

		// 回退本地订单状态并通知管理员
		s.rollbackLocalOrderOnProcurementFailure(procOrder, "upstream canceled order")

		logger.Infow("procurement_order_canceled_by_upstream",
			"procurement_order_id", procOrder.ID,
			"local_order_id", procOrder.LocalOrderID,
		)
	case "refunded", "partially_refunded":
		updates := map[string]interface{}{
			"updated_at": now,
		}
		if fulfillment != nil {
			updates["upstream_payload"] = fulfillment.Payload
		}
		targetStatus := constants.ProcurementStatusPartiallyRefunded
		if upstreamStatus == "refunded" {
			targetStatus = constants.ProcurementStatusRefunded
		}
		if err := s.procRepo.UpdateStatus(procOrder.ID, targetStatus, updates); err != nil {
			return fmt.Errorf("update procurement status: %w", err)
		}
		logger.Infow("procurement_order_refunded",
			"procurement_order_id", procOrder.ID,
			"local_order_id", procOrder.LocalOrderID,
			"upstream_status", upstreamStatus,
			"local_status", targetStatus,
		)

	default:
		logger.Warnw("procurement_unknown_upstream_status",
			"procurement_order_id", procOrder.ID,
			"upstream_status", upstreamStatus,
		)
	}

	return nil
}

// createUpstreamFulfillment 在本地订单上创建上游交付记录
func (s *Service) createUpstreamFulfillment(orderID uint, uf *procurementcontract.Fulfillment, now time.Time) error {
	return s.orderLifecycle.CreateUpstreamFulfillment(orderID, uf, now)
}
