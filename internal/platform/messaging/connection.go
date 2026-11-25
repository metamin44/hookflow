package messaging

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/metamin44/hookflow/internal/platform/config"
)

// Dial opens an application-managed AMQP connection. Library recovery is
// deliberately disabled: callers own reconnect, topology re-verification, and
// publisher channel generations.
func Dial(ctx context.Context, cfg config.RabbitMQ) (*amqp.Connection, error) {
	if ctx == nil {
		return nil, errors.New("dial rabbitmq: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("dial rabbitmq: %w", err)
	}
	if cfg.Heartbeat <= 0 {
		return nil, errors.New("dial rabbitmq: heartbeat must be positive")
	}
	if cfg.ConnectTimeout <= 0 {
		return nil, errors.New("dial rabbitmq: connect timeout must be positive")
	}
	if _, err := parseRabbitURI(cfg.URI); err != nil {
		return nil, err
	}

	dialer := &net.Dialer{Timeout: cfg.ConnectTimeout}
	var handshakeDone chan struct{}
	dial := func(network, address string) (net.Conn, error) {
		conn, err := dialer.DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		deadline := time.Now().Add(cfg.ConnectTimeout)
		if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
			deadline = contextDeadline
		}
		if err := conn.SetDeadline(deadline); err != nil {
			_ = conn.Close()
			return nil, err
		}

		handshakeDone = make(chan struct{})
		done := handshakeDone
		go func() {
			select {
			case <-ctx.Done():
				_ = conn.Close()
			case <-done:
			}
		}()
		return conn, nil
	}

	properties := amqp.NewConnectionProperties()
	if cfg.ConnectionName != "" {
		properties["connection_name"] = cfg.ConnectionName
	}
	conn, err := amqp.DialConfig(cfg.URI, amqp.Config{
		Heartbeat:  cfg.Heartbeat,
		Dial:       dial,
		Recovery:   nil,
		Properties: properties,
	})
	if handshakeDone != nil {
		close(handshakeDone)
	}
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, fmt.Errorf("dial rabbitmq: %w", contextErr)
		}
		return nil, errors.New("dial rabbitmq: connection or handshake failed")
	}
	if err := ctx.Err(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("dial rabbitmq: %w", err)
	}
	return conn, nil
}

// ConnectAndEnsure dials, declares and verifies the topology, and verifies the
// management-visible dead-letter policy. Any failure closes the connection.
func ConnectAndEnsure(ctx context.Context, cfg config.RabbitMQ) (*amqp.Connection, error) {
	setupCtx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()
	conn, err := Dial(setupCtx, cfg)
	if err != nil {
		return nil, err
	}
	stopWatch := WatchConnectionContext(setupCtx, conn)
	defer stopWatch()
	if err := EnsureTopology(setupCtx, conn, cfg); err != nil {
		_ = CloseChannelAndConnection(conn, nil, cfg.ConnectTimeout)
		return nil, fmt.Errorf("ensure rabbitmq topology: %w", err)
	}
	if err := EnsurePolicy(setupCtx, cfg); err != nil {
		_ = CloseChannelAndConnection(conn, nil, cfg.ConnectTimeout)
		return nil, fmt.Errorf("ensure rabbitmq policy: %w", err)
	}
	if err := setupCtx.Err(); err != nil {
		_ = CloseChannelAndConnection(conn, nil, cfg.ConnectTimeout)
		return nil, fmt.Errorf("ensure rabbitmq setup: %w", err)
	}
	return conn, nil
}

// WatchConnectionContext interrupts AMQP I/O on cancellation independently of
// application/channel ownership mutexes. The library's context-taking publish
// API does not interrupt an already-blocked socket write. Stop the watch after
// the guarded operation completes; an interrupted connection is never reused.
func WatchConnectionContext(ctx context.Context, conn *amqp.Connection) func() {
	if conn == nil {
		return func() {}
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.CloseDeadline(time.Now()) })
	return func() { stop() }
}

// CloseChannelAndConnection closes the channel first on the normal path, but
// independently aborts the connection if channel RPC or settlement stalls. Both
// phases share one absolute deadline, including waiting for ownership locks.
func CloseChannelAndConnection(conn *amqp.Connection, channel *amqp.Channel, budget time.Duration) error {
	if conn == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	stopWatch := WatchConnectionContext(ctx, conn)
	defer stopWatch()
	var closeErr error
	if channel != nil {
		if err := channel.Close(); err != nil && !errors.Is(err, amqp.ErrClosed) {
			closeErr = errors.New("close rabbitmq channel failed")
		}
	}
	deadline, _ := ctx.Deadline()
	if err := conn.CloseDeadline(deadline); err != nil && !errors.Is(err, amqp.ErrClosed) {
		closeErr = errors.New("close rabbitmq connection failed")
	}
	return closeErr
}

func parseRabbitURI(raw string) (amqp.URI, error) {
	const invalid = "dial rabbitmq: invalid AMQP URI"
	if raw == "" || strings.TrimSpace(raw) != raw {
		return amqp.URI{}, errors.New(invalid)
	}
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" || parsed.Hostname() == "" {
		return amqp.URI{}, errors.New(invalid)
	}
	if parsed.Scheme != "amqp" && parsed.Scheme != "amqps" {
		return amqp.URI{}, errors.New(invalid)
	}
	if parsed.Opaque != "" || parsed.User == nil || parsed.User.Username() == "" {
		return amqp.URI{}, errors.New(invalid)
	}
	if password, present := parsed.User.Password(); !present || password == "" {
		return amqp.URI{}, errors.New(invalid)
	}
	if parsed.Fragment != "" || strings.Contains(raw, "#") {
		return amqp.URI{}, errors.New(invalid)
	}
	if port := parsed.Port(); port != "" {
		value, err := strconv.ParseUint(port, 10, 16)
		if err != nil || value == 0 {
			return amqp.URI{}, errors.New(invalid)
		}
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return amqp.URI{}, errors.New(invalid)
	}
	for key := range query {
		if strings.EqualFold(key, "heartbeat") || strings.EqualFold(key, "connection_timeout") {
			return amqp.URI{}, errors.New(invalid)
		}
	}
	for _, character := range parsed.Path {
		if unicode.IsControl(character) {
			return amqp.URI{}, errors.New(invalid)
		}
	}

	uri, err := amqp.ParseURI(raw)
	if err != nil || uri.Host == "" || uri.Port <= 0 || uri.Port > 65535 {
		return amqp.URI{}, errors.New(invalid)
	}
	return uri, nil
}

func parseManagementURL(raw string) (*url.URL, error) {
	const invalid = "ensure rabbitmq policy: invalid management URL"
	if raw == "" || strings.TrimSpace(raw) != raw {
		return nil, errors.New(invalid)
	}
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" || parsed.Hostname() == "" {
		return nil, errors.New(invalid)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New(invalid)
	}
	if parsed.Opaque != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return nil, errors.New(invalid)
	}
	if port := parsed.Port(); port != "" {
		value, err := strconv.ParseUint(port, 10, 16)
		if err != nil || value == 0 {
			return nil, errors.New(invalid)
		}
	}
	return parsed, nil
}
