package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

// RabbitMQ environment variable names. The URI is required because it carries
// credentials; its value is never included in validation errors.
const (
	EnvRabbitMQURI                 = "HOOKFLOW_RABBITMQ_URI"
	EnvRabbitMQManagementURL       = "HOOKFLOW_RABBITMQ_MANAGEMENT_URL"
	EnvRabbitMQHeartbeat           = "HOOKFLOW_RABBITMQ_HEARTBEAT"
	EnvRabbitMQConnectTimeout      = "HOOKFLOW_RABBITMQ_CONNECT_TIMEOUT"
	EnvRabbitMQReconnectMinBackoff = "HOOKFLOW_RABBITMQ_RECONNECT_MIN_BACKOFF"
	EnvRabbitMQReconnectMaxBackoff = "HOOKFLOW_RABBITMQ_RECONNECT_MAX_BACKOFF"
)

var requiredRabbitMQKeys = []string{
	EnvRabbitMQURI,
	EnvRabbitMQManagementURL,
}

// RabbitMQ contains the broker connection and management settings. VHost is
// derived from the URI path rather than configured independently.
type RabbitMQ struct {
	URI                 string
	ManagementURL       string
	ConnectionName      string
	VHost               string
	Heartbeat           time.Duration
	ConnectTimeout      time.Duration
	ReconnectMinBackoff time.Duration
	ReconnectMaxBackoff time.Duration
}

// Validate checks the broker URI, management URL, derived vhost, and timing
// parameters without exposing secret-bearing URI values in errors.
func (r RabbitMQ) Validate() error {
	vhost, err := validateRabbitMQURI(r.URI)
	if err != nil {
		return err
	}
	if r.VHost == "" {
		return errors.New("rabbitmq vhost must not be empty")
	}
	if r.VHost != vhost {
		return errors.New("rabbitmq vhost must match the URI path")
	}
	if err := validateRabbitMQManagementURL(r.ManagementURL); err != nil {
		return err
	}
	if len(r.ConnectionName) > 255 || strings.ContainsAny(r.ConnectionName, "\r\n\x00") {
		return errors.New("rabbitmq connection name must be bounded and contain no control characters")
	}
	if r.Heartbeat <= 0 {
		return errors.New("rabbitmq heartbeat must be positive")
	}
	if r.ConnectTimeout <= 0 {
		return errors.New("rabbitmq connect timeout must be positive")
	}
	if r.ReconnectMinBackoff <= 0 {
		return errors.New("rabbitmq reconnect minimum backoff must be positive")
	}
	if r.ReconnectMaxBackoff <= 0 {
		return errors.New("rabbitmq reconnect maximum backoff must be positive")
	}
	if r.ReconnectMinBackoff > r.ReconnectMaxBackoff {
		return errors.New("rabbitmq reconnect minimum backoff must not exceed the maximum backoff")
	}
	return nil
}

func validateRabbitMQURI(raw string) (string, error) {
	uri := strings.TrimSpace(raw)
	if uri == "" {
		return "", errors.New("rabbitmq URI is required")
	}
	parsed, err := url.Parse(uri)
	if err != nil {
		return "", errors.New("rabbitmq URI must be a valid absolute amqp/amqps URL")
	}
	if !parsed.IsAbs() || parsed.Host == "" || parsed.Hostname() == "" {
		return "", errors.New("rabbitmq URI must be an absolute amqp/amqps URL with a host")
	}
	if !strings.EqualFold(parsed.Scheme, "amqp") && !strings.EqualFold(parsed.Scheme, "amqps") {
		return "", errors.New("rabbitmq URI scheme must be amqp or amqps")
	}
	if parsed.User == nil {
		return "", errors.New("rabbitmq URI must include a username and password")
	}
	password, hasPassword := parsed.User.Password()
	if parsed.User.Username() == "" || !hasPassword || password == "" {
		return "", errors.New("rabbitmq URI must include a nonempty username and password")
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return "", errors.New("rabbitmq URI query must be valid")
	}
	for key := range query {
		if strings.EqualFold(key, "heartbeat") || strings.EqualFold(key, "connection_timeout") {
			return "", errors.New("rabbitmq URI must not set heartbeat or connection_timeout query parameters")
		}
	}
	if parsed.Fragment != "" || strings.Contains(uri, "#") {
		return "", errors.New("rabbitmq URI must not include a fragment")
	}

	// net/url already decoded Path. amqp091-go strips the URI separator once;
	// decoding again would corrupt literal percent signs and encoded slashes.
	vhost := parsed.Path
	if vhost == "" || vhost == "/" {
		vhost = "/"
	} else {
		vhost = strings.TrimPrefix(vhost, "/")
	}
	if vhost == "" || strings.ContainsAny(vhost, "\r\n\x00") {
		return "", errors.New("rabbitmq URI vhost must be a valid nonempty path")
	}
	return vhost, nil
}

func validateRabbitMQManagementURL(raw string) error {
	managementURL := strings.TrimSpace(raw)
	if managementURL == "" {
		return errors.New("rabbitmq management URL is required")
	}
	parsed, err := url.Parse(managementURL)
	if err != nil {
		return errors.New("rabbitmq management URL must be a valid absolute HTTP(S) URL")
	}
	if !parsed.IsAbs() || parsed.Host == "" || parsed.Hostname() == "" {
		return errors.New("rabbitmq management URL must be an absolute HTTP(S) URL with a host")
	}
	if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return errors.New("rabbitmq management URL scheme must be http or https")
	}
	if parsed.User != nil {
		return errors.New("rabbitmq management URL must not include user information")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery || strings.Contains(managementURL, "?") {
		return errors.New("rabbitmq management URL must not include a query")
	}
	if parsed.Fragment != "" || strings.Contains(managementURL, "#") {
		return errors.New("rabbitmq management URL must not include a fragment")
	}
	return nil
}

// LoadRabbitMQ reads and validates the RabbitMQ configuration from the process
// environment. Duration settings use approved defaults when unset.
func LoadRabbitMQ() (RabbitMQ, error) {
	return loadRabbitMQ(os.LookupEnv)
}

func loadRabbitMQ(getenv func(string) (string, bool)) (RabbitMQ, error) {
	values, err := requireValues(getenv, requiredRabbitMQKeys)
	if err != nil {
		return RabbitMQ{}, err
	}

	uri := strings.TrimSpace(values[EnvRabbitMQURI])
	vhost, err := validateRabbitMQURI(uri)
	if err != nil {
		return RabbitMQ{}, err
	}
	rabbitmq := RabbitMQ{
		URI:                 uri,
		ManagementURL:       strings.TrimSpace(values[EnvRabbitMQManagementURL]),
		VHost:               vhost,
		Heartbeat:           10 * time.Second,
		ConnectTimeout:      5 * time.Second,
		ReconnectMinBackoff: 500 * time.Millisecond,
		ReconnectMaxBackoff: 10 * time.Second,
	}

	rabbitmq.Heartbeat, err = positiveDurationWithDefault(getenv, EnvRabbitMQHeartbeat, rabbitmq.Heartbeat)
	if err != nil {
		return RabbitMQ{}, err
	}
	rabbitmq.ConnectTimeout, err = positiveDurationWithDefault(getenv, EnvRabbitMQConnectTimeout, rabbitmq.ConnectTimeout)
	if err != nil {
		return RabbitMQ{}, err
	}
	rabbitmq.ReconnectMinBackoff, err = positiveDurationWithDefault(getenv, EnvRabbitMQReconnectMinBackoff, rabbitmq.ReconnectMinBackoff)
	if err != nil {
		return RabbitMQ{}, err
	}
	rabbitmq.ReconnectMaxBackoff, err = positiveDurationWithDefault(getenv, EnvRabbitMQReconnectMaxBackoff, rabbitmq.ReconnectMaxBackoff)
	if err != nil {
		return RabbitMQ{}, err
	}

	if err := rabbitmq.Validate(); err != nil {
		return RabbitMQ{}, fmt.Errorf("invalid rabbitmq configuration: %w", err)
	}
	return rabbitmq, nil
}
