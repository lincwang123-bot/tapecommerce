package productwrite

import (
	contract "github.com/dujiao-next/internal/modules/catalog/product/contract"
	product "github.com/dujiao-next/internal/modules/catalog/product/domain"
	"testing"
)

type modeProducts struct {
	ProductRepository
	p *product.Product
}

func (r modeProducts) GetByID(string) (*product.Product, error) { return r.p, nil }
func TestDeliveryModeCannotBeChangedByPUT(t *testing.T) {
	for _, current := range []string{"upstream", "auto", "manual"} {
		for _, next := range []string{"upstream", "auto", "manual"} {
			if current == next {
				continue
			}
			p := &product.Product{FulfillmentType: current, IsMapped: current == "upstream"}
			svc := NewWriteService(Options{Products: modeProducts{p: p}})
			if _, err := svc.Update("1", CreateProductInput{FulfillmentType: next}); err != contract.ErrFulfillmentInvalid {
				t.Fatalf("%s -> %s got %v", current, next, err)
			}
			if p.FulfillmentType != current {
				t.Fatal("mutated original delivery mode")
			}
		}
	}
}
