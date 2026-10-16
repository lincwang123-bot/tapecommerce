# TRON verifier

See ../evmchain/README.md for the unified four-network contract, primary sources and verified RPC facts. This package only calls fixed mainnet TronGrid wallet/getnowblock and walletsolidity gettransactioninfobyid/getnowblock/getblockbynum. It never broadcasts, signs, or builds a payable transaction.

Addresses must be canonical 34-character Base58Check with network byte 0x41 and double-SHA256 checksum. TxIDs are 64 hex without 0x. Base units have six decimals. Canonical solidified-block inclusion and timestamp equality are required in addition to receipt SUCCESS and an exact Transfer event. Request.MinBlockNumber is mandatory; no same-height payment can settle an invoice created at that head. Sender is derived from the event, not customer identity.
