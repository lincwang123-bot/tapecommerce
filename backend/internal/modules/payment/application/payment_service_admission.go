package application

import (
	"errors"
	paymentcontract "github.com/dujiao-next/internal/modules/payment/contract"
)

var ErrChainInvoiceLimit = errors.New("该订单已达到链上付款地址生成次数上限，请使用已有付款单或联系客服")

// Never recycle permanent transfer amounts. This bounds cross-chain switching
// on one order; existing invoices remain available for reconciliation.
func checkChainInvoiceLimit(store paymentcontract.Store, orderID uint) error {
	rows, err := store.ListByOrderID(orderID)
	if err != nil {
		return err
	}
	count := 0
	for _, p := range rows {
		if p.ProviderType == "selfhost" {
			count++
		}
	}
	if count >= 3 {
		return ErrChainInvoiceLimit
	}
	return nil
}

// Cheap preflight before ReadHead. The locked creation transaction repeats the
// count after checking reusable invoices, so concurrent requests cannot exceed it.
func (s *PaymentService) preflightChainInvoiceLimit(orderID, channelID uint) error {
	if channelID == 0 {
		return nil
	}
	c, err := s.channelRepo.GetByID(channelID)
	if err != nil {
		return err
	}
	if c == nil || c.ProviderType != "selfhost" {
		return nil
	}
	return s.paymentRepo.WithinTransaction(func(tx paymentcontract.Transaction) error {
		prior, err := s.reusableChainPayment(tx, orderID, c)
		if err != nil {
			return err
		}
		if prior != nil {
			return nil
		}
		return checkChainInvoiceLimit(tx.Payments(), orderID)
	})
}
