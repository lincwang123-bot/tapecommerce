// tapecommerce-demo is an ephemeral, local-only server-to-server bridge. It
// never loads config.yml, accepts an existing DB, or connects to a public RPC.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	tape "github.com/dujiao-next/internal/bootstrap/tapecommerce"
	"github.com/dujiao-next/internal/modules/tapecommerce/infrastructure/evmrpc"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "tapecommerce demo failed:", err)
		os.Exit(1)
	}
}
func run() error {
	secret := os.Getenv("TAPE_DEMO_SECRET")
	if len(secret) < 32 || os.Getenv("FACTORY_SHOP_ID") != "" || os.Getenv("VOUCHER_SERVICE_URL") != "" {
		return errors.New("isolated demo configuration required")
	}
	port := 0
	var err error
	if v := os.Getenv("TAPE_DEMO_PORT"); v != "" {
		port, err = strconv.Atoi(v)
		if err != nil || port < 0 || port > 65535 {
			return errors.New("invalid demo port")
		}
	}
	primary := os.Getenv("TAPE_DEMO_RPC_PRIMARY")
	secondary := os.Getenv("TAPE_DEMO_RPC_SECONDARY")
	if !loopbackRPC(primary) || !loopbackRPC(secondary) || primary == secondary {
		return errors.New("two distinct loopback RPC URLs required")
	}
	p, err := evmrpc.NewClient(primary)
	if err != nil {
		return err
	}
	s, err := evmrpc.NewClient(secondary)
	if err != nil {
		return err
	}
	// Synthetic card inventory still uses the original encrypted serializer.
	if err := os.Setenv("VOUCHER_DELIVERY_KEY", secret); err != nil {
		return err
	}
	demo, err := tape.OpenDemo(os.Getenv("TAPE_DEMO_TMP_ROOT"), tape.Bridge{Enabled: true, GuestSecret: secret, Checkout: os.Getenv("TAPE_DEMO_CHECKOUT"), Merchant: os.Getenv("TAPE_DEMO_MERCHANT"), TenantHash: os.Getenv("TAPE_DEMO_TENANT_HASH"), Verifier: evmrpc.Verifier{Primary: p, Witness: s}})
	if err != nil {
		return err
	}
	defer demo.Close()
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return err
	}
	defer listener.Close()
	server := &http.Server{Handler: tape.DemoHandler(demo, secret), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result := make(chan error, 1)
	go func() { result <- server.Serve(listener) }()
	if err := json.NewEncoder(os.Stdout).Encode(map[string]int{"port": listener.Addr().(*net.TCPAddr).Port}); err != nil {
		_ = server.Close()
		<-result
		return err
	}
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
		}
		<-result
	case err := <-result:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	return demo.Close()
}
func loopbackRPC(raw string) bool {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "http" || u.User != nil || u.Fragment != "" {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	return ip != nil && ip.IsLoopback()
}
