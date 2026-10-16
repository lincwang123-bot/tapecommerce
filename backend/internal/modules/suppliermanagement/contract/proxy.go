// Package suppliercontract defines the bounded internal management proxy.
package suppliercontract

import (
	"context"
	"encoding/json"
	"regexp"
)

const MaxRequestBytes = 64 << 10
const MaxResponseBytes = 2 << 20

type Failure struct {
	Status int
	Code   string
}

func (e *Failure) Error() string { return e.Code }

type Proxy interface {
	Forward(context.Context, string, string, []byte) (json.RawMessage, *Failure)
}

type Route struct{ Method, Path string }

var Routes = []Route{
	{"GET", "/suppliers"}, {"POST", "/suppliers"}, {"PUT", "/suppliers/:id"}, {"POST", "/suppliers/:id/check"},
	{"GET", "/supplier-products"}, {"GET", "/supplier-routes"}, {"POST", "/supplier-routes"}, {"PUT", "/supplier-routes/:id"},
	{"GET", "/supplier-policy"}, {"PUT", "/supplier-policy"}, {"POST", "/supplier-quotes"},
	{"GET", "/supplier-purchases"}, {"GET", "/supplier-purchases/:id"}, {"POST", "/supplier-purchases/:id/reconcile"},
}
var numericID = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)

func ValidID(id string) bool { return numericID.MatchString(id) }
