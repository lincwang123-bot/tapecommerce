package config

// TapeCommerceConfig is operator-only configuration. Disabled is the zero value;
// no demo database, wallet, faucet, signer key or synthetic identity is used.
type TapeCommerceConfig struct {
	Enabled          bool   `mapstructure:"enabled"`
	Migrate          bool   `mapstructure:"migrate"`
	ShopID           string `mapstructure:"shop_id"`
	Checkout         string `mapstructure:"checkout"`
	Merchant         string `mapstructure:"merchant"`
	TenantHash       string `mapstructure:"tenant_hash"`
	QuoteSigner      string `mapstructure:"quote_signer"`
	ChannelID        uint   `mapstructure:"channel_id"`
	RPCPrimary       string `mapstructure:"rpc_primary"`
	RPCSecondary     string `mapstructure:"rpc_secondary"`
	SignerURL        string `mapstructure:"signer_url"`
	SignerSecretFile string `mapstructure:"signer_secret_file"`
}
