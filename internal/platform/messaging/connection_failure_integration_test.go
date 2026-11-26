//go:build integration

package messaging

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/metamin44/hookflow/internal/platform/config"
)

const connectionFailureBudget = 150 * time.Millisecond

func TestWatchConnectionContextInterruptsChannelOpenIntegration(t *testing.T) {
	conn, proxy := connectionFailureFixture(t)
	proxy.gateMethod.Store(11) // channel.open-ok (class 20, method 11).

	type openResult struct {
		channel  *amqp.Channel
		err      error
		returned time.Time
	}
	result := make(chan openResult, 1)
	rpcDone := make(chan struct{})
	go func() {
		defer close(rpcDone)
		channel, err := conn.Channel()
		result <- openResult{channel: channel, err: err, returned: time.Now()}
	}()
	t.Cleanup(func() {
		// Also release and join the RPC if the watchdog regresses.
		proxy.close()
		_ = conn.CloseDeadline(time.Now())
		<-rpcDone
	})

	gateTimeout := time.NewTimer(5 * time.Second)
	defer gateTimeout.Stop()
	waitConnectionFailureGate(t, proxy, gateTimeout.C)
	select {
	case <-result:
		t.Fatal("Channel returned while its real channel.open-ok was withheld")
	default:
	}

	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), connectionFailureBudget)
	defer cancel()
	stopWatch := WatchConnectionContext(ctx, conn)
	defer stopWatch()
	abortTimeout := time.NewTimer(2 * time.Second)
	defer abortTimeout.Stop()

	select {
	case got := <-result:
		if got.err == nil || got.channel != nil {
			t.Fatal("Channel did not fail after the context deadline")
		}
		if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			t.Fatal("Channel failed before the context deadline")
		}
		if got.returned.Sub(started) >= 2*time.Second {
			t.Fatal("WatchConnectionContext took at least two seconds to abort Channel")
		}
	case <-abortTimeout.C:
		t.Fatal("WatchConnectionContext did not abort the blocked Channel RPC within two seconds")
	case failure := <-proxy.failures:
		t.Fatal(failure)
	}
	waitConnectionFailureRelease(t, conn, proxy, abortTimeout.C)
}

func TestCloseChannelAndConnectionBoundsBlockedChannelCloseIntegration(t *testing.T) {
	conn, proxy := connectionFailureFixture(t)
	setupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	stopWatch := WatchConnectionContext(setupCtx, conn)
	channel, err := conn.Channel()
	stopWatch()
	cancel()
	if err != nil {
		t.Fatal("open channel before the close regression test failed")
	}
	proxy.gateMethod.Store(41) // channel.close-ok (class 20, method 41).

	started := time.Now()
	result := make(chan time.Time, 1)
	rpcDone := make(chan struct{})
	go func() {
		defer close(rpcDone)
		// A graceful-close error is allowed; the contract is bounded release.
		_ = CloseChannelAndConnection(conn, channel, connectionFailureBudget)
		result <- time.Now()
	}()
	t.Cleanup(func() {
		proxy.close()
		_ = conn.CloseDeadline(time.Now())
		<-rpcDone
	})

	// Leave scheduling headroom, but not enough for a heartbeat timeout to
	// substitute for the 150ms teardown budget.
	closeTimeout := time.NewTimer(time.Second)
	defer closeTimeout.Stop()
	gatedAt := waitConnectionFailureGate(t, proxy, closeTimeout.C)
	select {
	case returnedAt := <-result:
		if !gatedAt.Before(returnedAt) {
			t.Fatal("channel.close-ok was not withheld before bounded teardown returned")
		}
		elapsed := returnedAt.Sub(started)
		if elapsed < connectionFailureBudget || elapsed >= time.Second {
			t.Fatal("CloseChannelAndConnection did not honor the 150ms budget within one second")
		}
	case <-closeTimeout.C:
		t.Fatal("CloseChannelAndConnection remained blocked beyond one second")
	case failure := <-proxy.failures:
		t.Fatal(failure)
	}
	waitConnectionFailureRelease(t, conn, proxy, closeTimeout.C)
	if !channel.IsClosed() {
		t.Fatal("bounded teardown left the channel open")
	}
}

func waitConnectionFailureGate(t *testing.T, proxy *connectionFailureProxy, timeout <-chan time.Time) time.Time {
	t.Helper()
	var gatedAt time.Time
	select {
	case gatedAt = <-proxy.gated:
	case failure := <-proxy.failures:
		t.Fatal(failure)
	case <-timeout:
		t.Fatal("proxy did not withhold the expected real broker channel acknowledgement")
	}
	return gatedAt
}

func waitConnectionFailureRelease(t *testing.T, conn *amqp.Connection, proxy *connectionFailureProxy, timeout <-chan time.Time) {
	t.Helper()
	select {
	case <-proxy.clientEOF:
	case failure := <-proxy.failures:
		t.Fatal(failure)
	case <-timeout:
		t.Fatal("AMQP connection did not release its TCP socket within the deadline")
	}
	if !conn.IsClosed() {
		t.Fatal("AMQP connection remained open after the interrupted RPC")
	}
}

func connectionFailureFixture(t *testing.T) (*amqp.Connection, *connectionFailureProxy) {
	t.Helper()
	cfg, err := config.LoadRabbitMQ()
	if err != nil {
		for _, key := range []string{config.EnvRabbitMQURI, config.EnvRabbitMQManagementURL} {
			if strings.TrimSpace(os.Getenv(key)) == "" {
				t.Skip("integration RabbitMQ configuration unavailable: missing required environment variables")
			}
		}
		t.Fatal("integration RabbitMQ configuration invalid")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	uri, err := parseRabbitURI(cfg.URI)
	if err != nil {
		t.Fatal("integration RabbitMQ URI invalid")
	}
	dialer := net.Dialer{Timeout: cfg.ConnectTimeout}
	brokerTCP, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(uri.Host, strconv.Itoa(uri.Port)))
	if err != nil {
		t.Fatal("connect proxy to real RabbitMQ broker failed")
	}
	t.Cleanup(func() { _ = brokerTCP.Close() })
	var broker net.Conn = brokerTCP
	if uri.Scheme == "amqps" {
		// Terminate upstream TLS so the proxy can identify actual AMQP replies;
		// the loopback side still uses real TCP and never synthesizes a frame.
		tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: uri.Host}
		if uri.ServerName != "" {
			tlsConfig.ServerName = uri.ServerName
		}
		if uri.CACertFile != "" {
			pem, err := os.ReadFile(uri.CACertFile)
			if err != nil {
				t.Fatal("read RabbitMQ proxy TLS CA failed")
			}
			tlsConfig.RootCAs = x509.NewCertPool()
			if !tlsConfig.RootCAs.AppendCertsFromPEM(pem) {
				t.Fatal("parse RabbitMQ proxy TLS CA failed")
			}
		}
		if uri.CertFile != "" && uri.KeyFile != "" {
			certificate, err := tls.LoadX509KeyPair(uri.CertFile, uri.KeyFile)
			if err != nil {
				t.Fatal("load RabbitMQ proxy TLS client certificate failed")
			}
			tlsConfig.Certificates = []tls.Certificate{certificate}
		}
		tlsBroker := tls.Client(brokerTCP, tlsConfig)
		if err := tlsBroker.HandshakeContext(ctx); err != nil {
			t.Fatal("RabbitMQ proxy TLS handshake failed")
		}
		broker = tlsBroker
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("listen on RabbitMQ proxy loopback socket failed")
	}
	proxy := &connectionFailureProxy{
		listener:  listener,
		broker:    broker,
		brokerTCP: brokerTCP,
		accepted:  make(chan net.Conn, 1),
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
		gated:     make(chan time.Time, 1),
		clientEOF: make(chan struct{}),
		failures:  make(chan string, 2),
	}
	go proxy.run()
	t.Cleanup(func() {
		proxy.close()
		select {
		case failure := <-proxy.failures:
			t.Fatal(failure)
		default:
		}
	})

	localURI, err := url.Parse(cfg.URI)
	if err != nil {
		t.Fatal("construct RabbitMQ proxy URI failed")
	}
	localURI.Scheme = "amqp"
	localURI.Host = listener.Addr().String()
	cfg.URI = localURI.String()
	conn, err := Dial(ctx, cfg)
	if err != nil {
		t.Fatal("RabbitMQ handshake through TCP proxy failed")
	}
	t.Cleanup(func() {
		proxy.close()
		_ = conn.CloseDeadline(time.Now())
	})
	stopWatch := WatchConnectionContext(ctx, conn)
	defer stopWatch()
	if err := EnsureTopology(ctx, conn, cfg); err != nil {
		t.Fatal("RabbitMQ topology setup through TCP proxy failed")
	}
	if ctx.Err() != nil || conn.IsClosed() {
		t.Fatal("RabbitMQ proxy setup did not finish with a live connection")
	}
	return conn, proxy
}

// Only the broker-to-client pump is gated. The opposite pump keeps forwarding
// real client RPCs, and gated receives a timestamp only after the expected broker
// reply has arrived. No application mutex, fabricated reply, or library mock is involved.
// All diagnostics are fixed strings: never include a URI, frame, or raw error.
type connectionFailureProxy struct {
	listener   net.Listener
	broker     net.Conn
	brokerTCP  net.Conn
	accepted   chan net.Conn
	stop       chan struct{}
	done       chan struct{}
	gated      chan time.Time
	clientEOF  chan struct{}
	failures   chan string
	gateMethod atomic.Uint32
	closeOnce  sync.Once
}

func (proxy *connectionFailureProxy) run() {
	defer close(proxy.done)
	defer close(proxy.accepted)
	client, err := proxy.listener.Accept()
	if err != nil {
		proxy.fail("accept RabbitMQ proxy TCP client failed")
		return
	}
	proxy.accepted <- client
	defer func() { _ = client.Close() }()

	brokerDone := make(chan struct{})
	go func() {
		defer close(brokerDone)
		proxy.forwardBroker(client)
	}()
	if _, err := io.Copy(proxy.broker, client); err != nil {
		proxy.fail("RabbitMQ proxy client-to-broker forwarding failed")
	}
	close(proxy.clientEOF)
	<-brokerDone
}

func (proxy *connectionFailureProxy) forwardBroker(client net.Conn) {
	for {
		var header [7]byte
		if _, err := io.ReadFull(proxy.broker, header[:]); err != nil {
			proxy.fail("read RabbitMQ proxy broker frame header failed")
			return
		}
		length := binary.BigEndian.Uint32(header[3:])
		if length > 1<<20 {
			proxy.fail("RabbitMQ proxy broker frame exceeded the test size bound")
			return
		}
		frame := make([]byte, 7+int(length)+1)
		copy(frame, header[:])
		if _, err := io.ReadFull(proxy.broker, frame[7:]); err != nil {
			proxy.fail("read RabbitMQ proxy broker frame payload failed")
			return
		}
		if frame[len(frame)-1] != 0xce {
			proxy.fail("RabbitMQ proxy received an invalid broker frame terminator")
			return
		}
		if header[0] == 1 && binary.BigEndian.Uint16(header[1:3]) != 0 && length >= 4 &&
			binary.BigEndian.Uint16(frame[7:9]) == 20 &&
			uint32(binary.BigEndian.Uint16(frame[9:11])) == proxy.gateMethod.Load() {
			proxy.gated <- time.Now()
			<-proxy.stop // Never forward the withheld acknowledgement, even in cleanup.
			return
		}
		if n, err := client.Write(frame); err != nil || n != len(frame) {
			proxy.fail("RabbitMQ proxy broker-to-client forwarding failed")
			return
		}
	}
}

func (proxy *connectionFailureProxy) fail(message string) {
	select {
	case <-proxy.stop:
		// Socket closure is expected only after cleanup has begun.
		return
	default:
		proxy.failures <- message
	}
}

func (proxy *connectionFailureProxy) close() {
	proxy.closeOnce.Do(func() {
		close(proxy.stop)
		_ = proxy.listener.Close()
		// Close the actual TCP socket first, including for a TLS upstream, so
		// cleanup cannot itself wait for a TLS close-notify exchange.
		_ = proxy.brokerTCP.Close()
		_ = proxy.broker.Close()
		if client, ok := <-proxy.accepted; ok {
			_ = client.Close()
		}
		<-proxy.done // run also joins the broker-to-client pump.
	})
}
