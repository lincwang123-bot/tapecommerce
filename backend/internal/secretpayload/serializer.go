package secretpayload

import (
	"context"
	"fmt"
	"github.com/dujiao-next/internal/crypto"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
	"os"
	"reflect"
	"strings"
)

type Serializer struct{}

func init() { schema.RegisterSerializer("delivery_secret", Serializer{}) }
func Encode(s string) (string, error) {
	if s == "" || strings.HasPrefix(s, "dv1:") {
		return s, nil
	}
	key := os.Getenv("VOUCHER_DELIVERY_KEY")
	if key == "" {
		if os.Getenv("VOUCHER_SERVICE_URL") != "" {
			return "", fmt.Errorf("VOUCHER_DELIVERY_KEY required")
		}
		return s, nil
	}
	enc, e := crypto.Encrypt(crypto.DeriveKey(key), s)
	return "dv1:" + enc, e
}
func Decode(s string) (string, error) {
	if !strings.HasPrefix(s, "dv1:") {
		return s, nil
	}
	if os.Getenv("VOUCHER_DELIVERY_KEY") == "" {
		return "", fmt.Errorf("VOUCHER_DELIVERY_KEY required")
	}
	return crypto.Decrypt(crypto.DeriveKey(os.Getenv("VOUCHER_DELIVERY_KEY")), strings.TrimPrefix(s, "dv1:"))
}
func (Serializer) Value(_ context.Context, _ *schema.Field, _ reflect.Value, value any) (any, error) {
	s, _ := value.(string)
	return Encode(s)
}
func (Serializer) Scan(ctx context.Context, field *schema.Field, dst reflect.Value, value any) error {
	var s string
	switch v := value.(type) {
	case string:
		s = v
	case []byte:
		s = string(v)
	case nil:
	default:
		return fmt.Errorf("invalid secret payload")
	}
	plain, e := Decode(s)
	if e != nil {
		return e
	}
	return field.Set(ctx, dst, plain)
}

// Register protects map-based updates too: GORM serializers only cover structs.
func Register(db *gorm.DB) {
	db.Callback().Update().Before("gorm:update").Register("delivery:encrypt-map", func(tx *gorm.DB) {
		m, ok := tx.Statement.Dest.(map[string]interface{})
		if !ok {
			return
		}
		var key string
		switch tx.Statement.Table {
		case "card_secrets":
			key = "secret"
		case "fulfillments":
			key = "payload"
		case "procurement_orders":
			key = "upstream_payload"
		default:
			return
		}
		if raw, ok := m[key].(string); ok {
			enc, e := Encode(raw)
			if e != nil {
				tx.AddError(e)
				return
			}
			m[key] = enc
		}
	})
}
