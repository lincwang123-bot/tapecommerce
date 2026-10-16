package evmrpc

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/dujiao-next/internal/modules/tapecommerce/domain"
	"golang.org/x/crypto/sha3"
)

// EventTopic matches the checked-in TapeCommerceCheckout Solidity ABI.
var EventTopic = eventTopic("CheckoutPaid(bytes32,address,address,uint256,bytes32)")

func eventTopic(signature string) string {
	h := sha3.NewLegacyKeccak256()
	_, _ = h.Write([]byte(signature))
	return "0x" + hex.EncodeToString(h.Sum(nil))
}

type logEntry struct {
	Address         string   `json:"address"`
	Topics          []string `json:"topics"`
	Data            string   `json:"data"`
	LogIndex        string   `json:"logIndex"`
	TransactionHash string   `json:"transactionHash"`
	BlockHash       string   `json:"blockHash"`
	BlockNumber     string   `json:"blockNumber"`
	Removed         bool     `json:"removed"`
}

type receipt struct {
	TransactionHash string     `json:"transactionHash"`
	Status          string     `json:"status"`
	To              string     `json:"to"`
	From            string     `json:"from"`
	BlockNumber     string     `json:"blockNumber"`
	BlockHash       string     `json:"blockHash"`
	Logs            []logEntry `json:"logs"`
}

type block struct {
	Number    string `json:"number"`
	Hash      string `json:"hash"`
	Timestamp string `json:"timestamp"`
}

// Verifier requires two independently configured RPC readers. A single RPC
// pretending that `safe` equals latest is insufficient to release stock here.
// Operators must separately validate X Layer's safe-tag settlement semantics;
// two agreeing RPCs do not constitute an L1 finality proof.
type Verifier struct {
	Primary RPC
	Witness RPC
}

func (v Verifier) Verify(ctx context.Context, invoice domain.Invoice, txHash string) (domain.Evidence, error) {
	if err := invoice.Validate(); err != nil {
		return domain.Evidence{}, err
	}
	if !domain.ValidHex(txHash, 32) || v.Primary == nil || v.Witness == nil {
		return domain.Evidence{}, domain.ErrInvalid
	}
	if a, ok := v.Primary.(*Client); ok {
		if b, ok := v.Witness.(*Client); ok && a.endpoint == b.endpoint {
			return domain.Evidence{}, domain.ErrInvalid
		}
	}
	invoice = invoice.Normalize()
	txHash = strings.ToLower(txHash)
	a, err := readEvidence(ctx, v.Primary, invoice, txHash)
	if err != nil {
		return a, err
	}
	b, err := readEvidence(ctx, v.Witness, invoice, txHash)
	if err != nil {
		return b, err
	}
	if a.TxHash != b.TxHash || a.BlockHash != b.BlockHash || a.BlockNumber != b.BlockNumber || a.LogIndex != b.LogIndex || a.PaidAt != b.PaidAt {
		return domain.Evidence{}, domain.ErrReorg
	}
	a.Safe = a.Safe && b.Safe
	if b.SafeBlock < a.SafeBlock {
		a.SafeBlock = b.SafeBlock
	}
	return a, nil
}

func readEvidence(ctx context.Context, rpc RPC, invoice domain.Invoice, txHash string) (domain.Evidence, error) {
	var chain string
	if err := rpc.Call(ctx, "eth_chainId", []any{}, &chain); err != nil {
		return domain.Evidence{}, err
	}
	chainID, err := quantity(chain)
	if err != nil || chainID != domain.ChainID {
		return domain.Evidence{}, domain.ErrReceipt
	}
	var r *receipt
	if err := rpc.Call(ctx, "eth_getTransactionReceipt", []any{txHash}, &r); err != nil {
		return domain.Evidence{}, err
	}
	if r == nil {
		return domain.Evidence{}, domain.ErrPending
	}
	evidence, err := receiptEvidence(invoice, txHash, *r)
	if err != nil {
		return evidence, err
	}
	var canonical *block
	if err := rpc.Call(ctx, "eth_getBlockByNumber", []any{r.BlockNumber, false}, &canonical); err != nil {
		return evidence, err
	}
	if canonical == nil || !strings.EqualFold(canonical.Hash, evidence.BlockHash) {
		return evidence, domain.ErrReorg
	}
	blockNumber, err := quantity(canonical.Number)
	if err != nil || blockNumber != evidence.BlockNumber {
		return evidence, domain.ErrReorg
	}
	timestamp, err := quantity(canonical.Timestamp)
	if err != nil || timestamp > invoice.Deadline || timestamp < invoice.FXSnapshot.IssuedAtMS/1000 {
		return evidence, domain.ErrReceipt
	}
	evidence.PaidAt = timestamp
	var safe *block
	if err := rpc.Call(ctx, "eth_getBlockByNumber", []any{"safe", false}, &safe); err != nil {
		return evidence, err
	}
	if safe == nil || !domain.ValidHex(safe.Hash, 32) {
		return evidence, domain.ErrRPC
	}
	safeNumber, err := quantity(safe.Number)
	if err != nil {
		return evidence, domain.ErrRPC
	}
	evidence.SafeBlock = safeNumber
	if safeNumber < evidence.BlockNumber {
		return evidence, nil
	}
	// Check safe itself is still on the canonical chain, then fetch the receipt
	// again. A block replaced during the sequence cannot release inventory.
	var safeCanonical *block
	if err := rpc.Call(ctx, "eth_getBlockByNumber", []any{safe.Number, false}, &safeCanonical); err != nil {
		return evidence, err
	}
	if safeCanonical == nil || !strings.EqualFold(safeCanonical.Hash, safe.Hash) {
		return evidence, domain.ErrReorg
	}
	var again *receipt
	if err := rpc.Call(ctx, "eth_getTransactionReceipt", []any{txHash}, &again); err != nil {
		return evidence, err
	}
	if again == nil {
		return evidence, domain.ErrReorg
	}
	checked, err := receiptEvidence(invoice, txHash, *again)
	if err != nil || checked.BlockHash != evidence.BlockHash || checked.LogIndex != evidence.LogIndex || checked.BlockNumber != evidence.BlockNumber {
		return evidence, domain.ErrReorg
	}
	evidence.Safe = true
	return evidence, nil
}

func receiptEvidence(invoice domain.Invoice, txHash string, r receipt) (domain.Evidence, error) {
	result := domain.Evidence{ChainID: domain.ChainID, TxHash: txHash, BlockHash: strings.ToLower(r.BlockHash)}
	status, err := quantity(r.Status)
	if err != nil || status != 1 || !strings.EqualFold(r.TransactionHash, txHash) || !strings.EqualFold(r.To, invoice.Checkout) || !strings.EqualFold(r.From, invoice.Payer) || !domain.ValidHex(r.BlockHash, 32) {
		return result, domain.ErrReceipt
	}
	result.BlockNumber, err = quantity(r.BlockNumber)
	if err != nil {
		return result, domain.ErrReceipt
	}
	found := 0
	for _, log := range r.Logs {
		if !strings.EqualFold(log.Address, invoice.Checkout) || len(log.Topics) == 0 || !strings.EqualFold(log.Topics[0], EventTopic) {
			continue
		}
		if len(log.Topics) != 4 || log.Removed || !strings.EqualFold(log.TransactionHash, txHash) || !strings.EqualFold(log.BlockHash, r.BlockHash) || log.BlockNumber != r.BlockNumber {
			return result, domain.ErrReceipt
		}
		if !strings.EqualFold(log.Topics[1], invoice.InvoiceID) || !addressTopic(log.Topics[2], invoice.Payer) || !addressTopic(log.Topics[3], invoice.Merchant) {
			return result, domain.ErrReceipt
		}
		if !domain.ValidHex(log.Data, 64) {
			return result, domain.ErrReceipt
		}
		amount, ok := new(big.Int).SetString(log.Data[2:66], 16)
		if !ok || amount.String() != invoice.AmountWei || !strings.EqualFold("0x"+log.Data[66:], invoice.TenantHash) {
			return result, domain.ErrReceipt
		}
		result.LogIndex, err = quantity(log.LogIndex)
		if err != nil {
			return result, domain.ErrReceipt
		}
		found++
	}
	if found != 1 {
		return result, domain.ErrReceipt
	}
	return result, nil
}

func addressTopic(topic, address string) bool {
	return domain.ValidHex(topic, 32) && strings.HasPrefix(topic[2:], strings.Repeat("0", 24)) && strings.EqualFold("0x"+topic[26:], address)
}

func quantity(s string) (uint64, error) {
	if !strings.HasPrefix(s, "0x") || len(s) < 3 || (len(s) > 3 && s[2] == '0') {
		return 0, fmt.Errorf("invalid RPC quantity")
	}
	return strconv.ParseUint(s[2:], 16, 64)
}
