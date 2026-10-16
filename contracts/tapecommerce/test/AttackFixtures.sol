// SPDX-License-Identifier: MIT
pragma solidity 0.8.30;

import {TapeCommerceCheckout} from "../src/TapeCommerceCheckout.sol";

contract RejectingMerchant {
    bool public reject = true;
    function setReject(bool next) external { reject = next; }
    receive() external payable { require(!reject, "reject"); }
}

contract ReentrantMerchant {
    TapeCommerceCheckout public immutable checkout;
    bytes private nestedCall;
    uint256 private nestedValue;
    bool public attempted;
    bool public nestedSucceeded;
    bytes4 public nestedError;
    bool private propagate;

    constructor(TapeCommerceCheckout target) { checkout = target; }

    function configure(bytes calldata data, uint256 value, bool shouldPropagate) external {
        nestedCall = data;
        nestedValue = value;
        propagate = shouldPropagate;
    }

    receive() external payable {
        attempted = true;
        (bool success, bytes memory result) = address(checkout).call{value: nestedValue}(nestedCall);
        nestedSucceeded = success;
        if (result.length >= 4) nestedError = bytes4(result);
        if (propagate) require(success, "nested call rejected");
    }
}

contract ForceCredit {
    constructor(address payable recipient) payable {
        selfdestruct(recipient);
    }
}
