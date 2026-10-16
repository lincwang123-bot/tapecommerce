package evmchain

type Network string

const (
	BSC      Network = "bsc"
	Ethereum Network = "ethereum"
	Polygon  Network = "polygon"
)

type NetworkSpec struct {
	Network       Network
	ChainID       uint64
	TokenContract string
	Decimals      uint8
	Endpoint      string
	chainHex      string
}

// Specification returns a copy from a closed server-controlled registry.
func Specification(network Network) (NetworkSpec, bool) {
	switch network {
	case BSC:
		return NetworkSpec{BSC, 56, USDTContract, 18, Endpoint, "0x38"}, true
	case Ethereum:
		return NetworkSpec{Ethereum, 1, "0xdac17f958d2ee523a2206206994597c13d831ec7", 6, "https://ethereum-rpc.publicnode.com", "0x1"}, true
	case Polygon:
		return NetworkSpec{Polygon, 137, "0xc2132d05d31c914a87c6611c10748aeb04b58e8f", 6, "https://polygon.drpc.org", "0x89"}, true
	default:
		return NetworkSpec{}, false
	}
}
