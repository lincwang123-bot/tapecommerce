package adminauthwiring

import (
	"context"
	"fmt"
	"github.com/dujiao-next/internal/cache"
	"github.com/dujiao-next/internal/config"
	"net"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestChallengeConsumptionAtomicAndFailClosed(t *testing.T) {
	binary, err := exec.LookPath("redis-server")
	if err != nil {
		t.Skip("redis-server unavailable")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	cmd := exec.Command(binary, "--bind", "127.0.0.1", "--port", strconv.Itoa(port), "--save", "", "--appendonly", "no", "--dir", t.TempDir())
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Logf("temporary redis PID=%d", cmd.Process.Pid)
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		_ = cache.InitRedis(&config.RedisConfig{Enabled: false})
	})
	if err := cache.InitRedis(&config.RedisConfig{Enabled: true, Host: "127.0.0.1", Port: port}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	client := cache.Client()
	defer client.Close()
	deadline := time.Now().Add(3 * time.Second)
	for client.Ping(ctx).Err() != nil {
		if time.Now().After(deadline) {
			t.Fatal("test Redis did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	store := admin2FAChallengeStoreAdapter{}
	jti := fmt.Sprint(time.Now().UnixNano())
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if store.Consume(ctx, jti) {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 || !store.IsRevoked(ctx, jti) {
		t.Fatalf("challenge winners=%d", successes.Load())
	}
	for i := int64(1); i <= 5; i++ {
		if n := store.BumpFails(ctx, "failure-fixture"); n != i {
			t.Fatalf("failure budget=%d want=%d", n, i)
		}
	}
	_ = client.Shutdown(ctx).Err()
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	stopped = true
	t.Logf("temporary redis PID=%d exited", cmd.Process.Pid)
	_ = cache.InitRedis(&config.RedisConfig{Enabled: false})
	if store.Consume(ctx, "unavailable") || !store.IsRevoked(ctx, "unavailable") || store.BumpFails(ctx, "unavailable") < 5 {
		t.Fatal("unavailable security store did not fail closed")
	}
}
