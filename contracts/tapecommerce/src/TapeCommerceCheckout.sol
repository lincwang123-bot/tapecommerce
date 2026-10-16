// SPDX-License-Identifier: MIT
pragma solidity 0.8.30;

import {Ownable} from "@openzeppelin/contracts/access/Ownable.sol";
import {Ownable2Step} from "@openzeppelin/contracts/access/Ownable2Step.sol";
import {EIP712} from "@openzeppelin/contracts/utils/cryptography/EIP712.sol";
import {ECDSA} from "@openzeppelin/contracts/utils/cryptography/ECDSA.sol";
import {ReentrancyGuard} from "@openzeppelin/contracts/utils/ReentrancyGuard.sol";

/// @notice Exact-value, noncustodial native OKB checkout for X Layer (196).
/// @dev The owner can rotate the EOA quote signer, but has no withdrawal power.
///      All valid payments are forwarded atomically; delivery remains off-chain.
contract TapeCommerceCheckout is EIP712, Ownable2Step, ReentrancyGuard {
    uint256 public constant CHAIN_ID = 196;
    bytes32 public constant INVOICE_TYPEHASH = keccak256(
        "Invoice(bytes32 invoiceId,bytes32 tenantHash,address payer,address merchant,uint256 amountWei,uint64 deadline)"
    );

    struct Invoice {
        bytes32 invoiceId;
        bytes32 tenantHash;
        address payer;
        address merchant;
        uint256 amountWei;
        uint64 deadline;
    }

    address public quoteSigner;
    mapping(bytes32 invoiceId => bool paid) public paidInvoices;

    error WrongChain(uint256 actualChainId);
    error InvalidQuoteSigner();
    error InvalidInvoiceId();
    error InvalidTenantHash();
    error InvalidMerchant();
    error InvalidAmount();
    error WrongPayer();
    error IncorrectValue(uint256 expected, uint256 actual);
    error InvoiceExpired();
    error InvoiceAlreadyPaid();
    error InvalidSignature();
    error MerchantTransferFailed();
    error DirectPaymentUnsupported();

    event CheckoutPaid(
        bytes32 indexed invoiceId,
        address indexed payer,
        address indexed merchant,
        uint256 amountWei,
        bytes32 tenantHash
    );
    event QuoteSignerChanged(address indexed previousSigner, address indexed newSigner);

    constructor(address initialOwner, address initialQuoteSigner)
        EIP712("TapeCommerceCheckout", "1")
        Ownable(initialOwner)
    {
        _requireChain();
        _setQuoteSigner(initialQuoteSigner);
    }

    /// @notice Old-signer quotes stop working immediately after rotation.
    /// @dev Owner should be a secured multisig, independently of the quote signer.
    function setQuoteSigner(address newSigner) external onlyOwner {
        _setQuoteSigner(newSigner);
    }

    function payOKB(Invoice calldata invoice, bytes calldata signature) external payable nonReentrant {
        _requireChain();
        if (invoice.invoiceId == bytes32(0)) revert InvalidInvoiceId();
        if (invoice.tenantHash == bytes32(0)) revert InvalidTenantHash();
        if (msg.sender != invoice.payer) revert WrongPayer();
        if (invoice.merchant == address(0) || invoice.merchant == address(this)) revert InvalidMerchant();
        if (invoice.amountWei == 0) revert InvalidAmount();
        if (msg.value != invoice.amountWei) revert IncorrectValue(invoice.amountWei, msg.value);
        if (block.timestamp > invoice.deadline) revert InvoiceExpired();
        if (paidInvoices[invoice.invoiceId]) revert InvoiceAlreadyPaid();

        (address recovered, ECDSA.RecoverError err,) = ECDSA.tryRecover(hashInvoice(invoice), signature);
        if (err != ECDSA.RecoverError.NoError || recovered != quoteSigner) revert InvalidSignature();

        // Effects precede the sole external interaction; a failed transfer rolls
        // back this flag and returns msg.value to the payer (excluding gas).
        paidInvoices[invoice.invoiceId] = true;
        (bool sent,) = payable(invoice.merchant).call{value: invoice.amountWei}("");
        if (!sent) revert MerchantTransferFailed();

        emit CheckoutPaid(invoice.invoiceId, invoice.payer, invoice.merchant, invoice.amountWei, invoice.tenantHash);
    }

    function hashInvoice(Invoice calldata invoice) public view returns (bytes32) {
        return _hashTypedDataV4(keccak256(abi.encode(
            INVOICE_TYPEHASH,
            invoice.invoiceId,
            invoice.tenantHash,
            invoice.payer,
            invoice.merchant,
            invoice.amountWei,
            invoice.deadline
        )));
    }

    /// @dev Accidental direct transfers revert. Forced EVM credits cannot be
    ///      prevented and have no recovery path; never intentionally force funds.
    receive() external payable {
        revert DirectPaymentUnsupported();
    }

    function _setQuoteSigner(address newSigner) private {
        if (newSigner == address(0)) revert InvalidQuoteSigner();
        address previousSigner = quoteSigner;
        quoteSigner = newSigner;
        emit QuoteSignerChanged(previousSigner, newSigner);
    }

    function _requireChain() private view {
        if (block.chainid != CHAIN_ID) revert WrongChain(block.chainid);
    }
}
