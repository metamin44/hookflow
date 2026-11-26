package messaging

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/metamin44/hookflow/internal/platform/config"
)

const (
	testAMQPUsername = "hookflow-secret-user-7f3c"
	testAMQPPassword = "hookflow-secret-password-9a2d"
)

func approvedMessagingConfig(uri, managementURL string) config.RabbitMQ {
	return config.RabbitMQ{
		URI:                 uri,
		ManagementURL:       managementURL,
		Heartbeat:           10 * time.Second,
		ConnectTimeout:      2 * time.Second,
		ReconnectMinBackoff: 500 * time.Millisecond,
		ReconnectMaxBackoff: 10 * time.Second,
	}
}

func TestDialRejectsInvalidConfigurationAndURI(t *testing.T) {
	validURI := "amqp://" + testAMQPUsername + ":" + testAMQPPassword + "@rabbit.example.com/team%2Fblue"
	tests := []struct {
		name   string
		mutate func(*config.RabbitMQ)
	}{
		{name: "empty URI", mutate: func(c *config.RabbitMQ) { c.URI = "" }},
		{name: "URI whitespace", mutate: func(c *config.RabbitMQ) { c.URI = " " + validURI }},
		{name: "unsupported scheme", mutate: func(c *config.RabbitMQ) {
			c.URI = "http://" + testAMQPUsername + ":" + testAMQPPassword + "@rabbit.example.com"
		}},
		{name: "opaque URI", mutate: func(c *config.RabbitMQ) {
			c.URI = "amqp:" + testAMQPUsername + ":" + testAMQPPassword + "@rabbit.example.com"
		}},
		{name: "missing host", mutate: func(c *config.RabbitMQ) { c.URI = "amqp://" + testAMQPUsername + ":" + testAMQPPassword + "@" }},
		{name: "missing credentials", mutate: func(c *config.RabbitMQ) { c.URI = "amqp://rabbit.example.com" }},
		{name: "empty username", mutate: func(c *config.RabbitMQ) { c.URI = "amqp://:" + testAMQPPassword + "@rabbit.example.com" }},
		{name: "missing password", mutate: func(c *config.RabbitMQ) { c.URI = "amqp://" + testAMQPUsername + "@rabbit.example.com" }},
		{name: "empty password", mutate: func(c *config.RabbitMQ) { c.URI = "amqp://" + testAMQPUsername + ":@rabbit.example.com" }},
		{name: "zero port", mutate: func(c *config.RabbitMQ) {
			c.URI = "amqp://" + testAMQPUsername + ":" + testAMQPPassword + "@rabbit.example.com:0"
		}},
		{name: "port overflow", mutate: func(c *config.RabbitMQ) {
			c.URI = "amqp://" + testAMQPUsername + ":" + testAMQPPassword + "@rabbit.example.com:65536"
		}},
		{name: "heartbeat query", mutate: func(c *config.RabbitMQ) { c.URI = validURI + "?heartbeat=30" }},
		{name: "mixed case heartbeat query", mutate: func(c *config.RabbitMQ) { c.URI = validURI + "?Heartbeat=30" }},
		{name: "encoded heartbeat query", mutate: func(c *config.RabbitMQ) { c.URI = validURI + "?%68eartbeat=30" }},
		{name: "connection timeout query", mutate: func(c *config.RabbitMQ) { c.URI = validURI + "?connection_timeout=5000" }},
		{name: "fragment", mutate: func(c *config.RabbitMQ) { c.URI = validURI + "#fragment" }},
		{name: "invalid query escape", mutate: func(c *config.RabbitMQ) { c.URI = validURI + "?other=%zz" }},
		{name: "invalid path escape", mutate: func(c *config.RabbitMQ) {
			c.URI = "amqp://" + testAMQPUsername + ":" + testAMQPPassword + "@rabbit.example.com/%zz"
		}},
		{name: "zero heartbeat", mutate: func(c *config.RabbitMQ) { c.Heartbeat = 0 }},
		{name: "zero connect timeout", mutate: func(c *config.RabbitMQ) { c.ConnectTimeout = 0 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := approvedMessagingConfig(validURI, "http://management.example.com:15672")
			tt.mutate(&cfg)
			_, err := Dial(context.Background(), cfg)
			if err == nil {
				t.Fatal("Dial() accepted invalid configuration")
			}
			assertNoSecret(t, err, cfg.URI, testAMQPUsername, testAMQPPassword)
		})
	}
}

func TestDialCancellationClosesHandshakeSocket(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	accepted := make(chan net.Conn, 1)
	acceptErrors := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			acceptErrors <- err
			return
		}
		accepted <- conn
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	cfg := approvedMessagingConfig("amqp://"+testAMQPUsername+":"+testAMQPPassword+"@"+listener.Addr().String(), "http://management.example.com:15672")
	started := time.Now()
	_, err = Dial(ctx, cfg)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Dial() error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Dial() did not honor cancellation promptly: %s", elapsed)
	}
	assertNoSecret(t, err, cfg.URI, testAMQPUsername, testAMQPPassword)

	var conn net.Conn
	select {
	case conn = <-accepted:
	case acceptErr := <-acceptErrors:
		t.Fatalf("Accept() error = %v", acceptErr)
	case <-time.After(time.Second):
		t.Fatal("Dial() did not connect to the test listener")
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("SetReadDeadline() error = %v", err)
	}
	if _, err := io.Copy(io.Discard, conn); err != nil {
		var networkError net.Error
		if errors.As(err, &networkError) && networkError.Timeout() {
			t.Fatal("handshake socket remained open after cancellation")
		}
	}
}

// delayedSignalContext models the bounded interval between a deadline passing
// and the context runtime publishing Done/Err. The socket deadline can fire
// inside that interval, so Dial must classify the reached deadline without
// relying on ctx.Err() having become visible yet.
type delayedSignalContext struct {
	context.Context
	deadline time.Time
}

func (c delayedSignalContext) Deadline() (time.Time, bool) { return c.deadline, true }
func (c delayedSignalContext) Done() <-chan struct{}       { return nil }
func (c delayedSignalContext) Err() error                  { return nil }

func TestDialReportsElapsedDeadlineWithoutContextSignal(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = io.Copy(io.Discard, conn)
	}()

	ctx := delayedSignalContext{
		Context:  context.Background(),
		deadline: time.Now().Add(100 * time.Millisecond),
	}
	cfg := approvedMessagingConfig("amqp://"+testAMQPUsername+":"+testAMQPPassword+"@"+listener.Addr().String(), "http://management.example.com:15672")
	started := time.Now()
	_, err = Dial(ctx, cfg)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Dial() error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Dial() did not honor the socket deadline promptly: %s", elapsed)
	}
	assertNoSecret(t, err, cfg.URI, testAMQPUsername, testAMQPPassword)
}

func TestDialContextErrorClassification(t *testing.T) {
	past := time.Now().Add(-time.Second)
	future := time.Now().Add(time.Minute)
	canceled, cancelCanceled := context.WithCancel(context.Background())
	cancelCanceled()
	expired, cancelExpired := context.WithDeadline(context.Background(), past)
	defer cancelExpired()
	active, cancelActive := context.WithDeadline(context.Background(), future)
	defer cancelActive()

	tests := []struct {
		name string
		ctx  context.Context
		want error
	}{
		{name: "no deadline", ctx: context.Background()},
		{name: "active deadline", ctx: active},
		{name: "canceled", ctx: canceled, want: context.Canceled},
		{name: "visible deadline", ctx: expired, want: context.DeadlineExceeded},
		{name: "lagging deadline", ctx: delayedSignalContext{Context: context.Background(), deadline: past}, want: context.DeadlineExceeded},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := dialContextError(tc.ctx)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("dialContextError() = %v, want nil", got)
				}
				return
			}
			if !errors.Is(got, tc.want) {
				t.Fatalf("dialContextError() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDialFailureDoesNotExposeSecrets(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	address := listener.Addr().String()
	_ = listener.Close()

	cfg := approvedMessagingConfig("amqp://"+testAMQPUsername+":"+testAMQPPassword+"@"+address, "http://management.example.com:15672")
	cfg.ConnectTimeout = 200 * time.Millisecond
	_, err = Dial(context.Background(), cfg)
	if err == nil {
		t.Fatal("Dial() unexpectedly succeeded without a listener")
	}
	assertNoSecret(t, err, cfg.URI, testAMQPUsername, testAMQPPassword)
	if strings.Contains(err.Error(), address) {
		t.Fatalf("Dial() exposed the broker address: %v", err)
	}
}

func TestDialNilAndCanceledContexts(t *testing.T) {
	cfg := approvedMessagingConfig("amqp://"+testAMQPUsername+":"+testAMQPPassword+"@rabbit.example.com", "http://management.example.com:15672")
	if _, err := Dial(nil, cfg); err == nil { //nolint:staticcheck // Nil context rejection is the behavior under test.
		t.Fatal("Dial() accepted a nil context")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Dial(ctx, cfg)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Dial() error = %v, want context canceled", err)
	}
}

func assertNoSecret(t *testing.T, err error, secrets ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, secret := range secrets {
		if secret != "" && strings.Contains(err.Error(), secret) {
			t.Fatalf("error disclosed a secret: %v", err)
		}
	}
}
