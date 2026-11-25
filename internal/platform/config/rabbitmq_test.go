package config

import (
	"strings"
	"testing"
	"time"
)

const (
	testRabbitMQUser     = "hookflow-secret-user-7f3c"
	testRabbitMQPassword = "hookflow-secret-password-9a2d"
	testRabbitMQURI      = "amqp://" + testRabbitMQUser + ":" + testRabbitMQPassword + "@rabbit.example.com"
)

func approvedRabbitMQ() RabbitMQ {
	return RabbitMQ{
		URI:                 testRabbitMQURI,
		ManagementURL:       "http://rabbit.example.com:15672",
		VHost:               "/",
		Heartbeat:           10 * time.Second,
		ConnectTimeout:      5 * time.Second,
		ReconnectMinBackoff: 500 * time.Millisecond,
		ReconnectMaxBackoff: 10 * time.Second,
	}
}

func validRabbitMQEnv() map[string]string {
	return map[string]string{
		EnvRabbitMQURI:           testRabbitMQURI,
		EnvRabbitMQManagementURL: "http://rabbit.example.com:15672",
	}
}

func TestLoadRabbitMQDefaultsAndOverrides(t *testing.T) {
	rabbitmq, err := loadRabbitMQ(lookup(validRabbitMQEnv()))
	if err != nil {
		t.Fatalf("loadRabbitMQ() defaults error: %v", err)
	}
	if want := approvedRabbitMQ(); rabbitmq != want {
		t.Fatalf("rabbitmq = %+v, want %+v", rabbitmq, want)
	}

	values := validRabbitMQEnv()
	values[EnvRabbitMQURI] = "amqps://" + testRabbitMQUser + ":" + testRabbitMQPassword + "@rabbit.example.com/team%2Fblue"
	values[EnvRabbitMQManagementURL] = "https://management.example.com/api"
	values[EnvRabbitMQHeartbeat] = "20s"
	values[EnvRabbitMQConnectTimeout] = "8s"
	values[EnvRabbitMQReconnectMinBackoff] = "1s"
	values[EnvRabbitMQReconnectMaxBackoff] = "30s"
	rabbitmq, err = loadRabbitMQ(lookup(values))
	if err != nil {
		t.Fatalf("loadRabbitMQ() overrides error: %v", err)
	}
	want := RabbitMQ{
		URI:                 values[EnvRabbitMQURI],
		ManagementURL:       "https://management.example.com/api",
		VHost:               "team/blue",
		Heartbeat:           20 * time.Second,
		ConnectTimeout:      8 * time.Second,
		ReconnectMinBackoff: time.Second,
		ReconnectMaxBackoff: 30 * time.Second,
	}
	if rabbitmq != want {
		t.Fatalf("rabbitmq = %+v, want %+v", rabbitmq, want)
	}
}

func TestLoadRabbitMQRequiresEndpoints(t *testing.T) {
	for _, key := range requiredRabbitMQKeys {
		for _, value := range []string{"", " \t\n "} {
			t.Run(key+"/"+value, func(t *testing.T) {
				values := validRabbitMQEnv()
				if value == "" {
					delete(values, key)
				} else {
					values[key] = value
				}
				_, err := loadRabbitMQ(lookup(values))
				if err == nil || !strings.Contains(err.Error(), key) {
					t.Fatalf("missing %s error = %v", key, err)
				}
			})
		}
	}
	if _, err := loadRabbitMQ(lookup(map[string]string{})); err == nil {
		t.Fatal("missing RabbitMQ endpoints accepted")
	}
}

func TestLoadRabbitMQNormalizesSurroundingWhitespace(t *testing.T) {
	values := validRabbitMQEnv()
	values[EnvRabbitMQURI] = "  " + testRabbitMQURI + "  "
	values[EnvRabbitMQManagementURL] = "  http://rabbit.example.com:15672  "
	values[EnvRabbitMQHeartbeat] = " 15s "
	rabbitmq, err := loadRabbitMQ(lookup(values))
	if err != nil {
		t.Fatalf("loadRabbitMQ() error: %v", err)
	}
	if rabbitmq.URI != testRabbitMQURI || rabbitmq.ManagementURL != "http://rabbit.example.com:15672" || rabbitmq.Heartbeat != 15*time.Second {
		t.Fatalf("unexpected normalized RabbitMQ configuration: %+v", rabbitmq)
	}
}

func TestLoadRabbitMQRejectsInvalidDurations(t *testing.T) {
	fields := []string{
		EnvRabbitMQHeartbeat,
		EnvRabbitMQConnectTimeout,
		EnvRabbitMQReconnectMinBackoff,
		EnvRabbitMQReconnectMaxBackoff,
	}
	for _, key := range fields {
		t.Run(key, func(t *testing.T) {
			assertInvalidRuntimeValue(t, key, false, func(overrides map[string]string) error {
				values := validRabbitMQEnv()
				for overrideKey, value := range overrides {
					values[overrideKey] = value
				}
				_, err := loadRabbitMQ(lookup(values))
				return err
			})
		})
	}

	values := validRabbitMQEnv()
	values[EnvRabbitMQReconnectMinBackoff] = "11s"
	if _, err := loadRabbitMQ(lookup(values)); err == nil {
		t.Fatal("reconnect minimum backoff above maximum accepted")
	}
}

func TestLoadRabbitMQDerivesVHost(t *testing.T) {
	cases := []struct {
		name string
		uri  string
		want string
	}{
		{"default", testRabbitMQURI, "/"},
		{"root", testRabbitMQURI + "/", "/"},
		{"plain", testRabbitMQURI + "/production", "production"},
		{"escaped slash", testRabbitMQURI + "/team%2Fblue", "team/blue"},
		{"escaped default slash", testRabbitMQURI + "/%2F", "/"},
		{"literal percent", testRabbitMQURI + "/%25", "%"},
		{"literal encoded slash", testRabbitMQURI + "/%252F", "%2F"},
		{"escaped unicode", testRabbitMQURI + "/%E2%9C%93", "✓"},
		{"other query", testRabbitMQURI + "/production?mode=publisher", "production"},
		{"IPv6 host", "amqp://" + testRabbitMQUser + ":" + testRabbitMQPassword + "@[::1]:5672/vhost", "vhost"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			values := validRabbitMQEnv()
			values[EnvRabbitMQURI] = tc.uri
			rabbitmq, err := loadRabbitMQ(lookup(values))
			if err != nil {
				t.Fatalf("loadRabbitMQ() error: %v", err)
			}
			if rabbitmq.VHost != tc.want {
				t.Fatalf("vhost = %q, want %q", rabbitmq.VHost, tc.want)
			}
		})
	}
}

func TestLoadRabbitMQRejectsInvalidURI(t *testing.T) {
	cases := []struct {
		name string
		uri  string
	}{
		{"relative", "rabbit.example.com/vhost"},
		{"unsupported scheme", "http://" + testRabbitMQUser + ":" + testRabbitMQPassword + "@rabbit.example.com"},
		{"opaque", "amqp:" + testRabbitMQUser + ":" + testRabbitMQPassword + "@rabbit.example.com"},
		{"missing host", "amqp://" + testRabbitMQUser + ":" + testRabbitMQPassword + "@"},
		{"missing credentials", "amqp://rabbit.example.com"},
		{"empty username", "amqp://:" + testRabbitMQPassword + "@rabbit.example.com"},
		{"missing password", "amqp://" + testRabbitMQUser + "@rabbit.example.com"},
		{"empty password", "amqp://" + testRabbitMQUser + ":@rabbit.example.com"},
		{"heartbeat query", testRabbitMQURI + "?heartbeat=30"},
		{"connection timeout query", testRabbitMQURI + "?connection_timeout=5000"},
		{"mixed case heartbeat query", testRabbitMQURI + "?Heartbeat=30"},
		{"encoded heartbeat query", testRabbitMQURI + "?%68eartbeat=30"},
		{"empty heartbeat query", testRabbitMQURI + "?heartbeat="},
		{"invalid query escape", testRabbitMQURI + "?other=%zz"},
		{"fragment", testRabbitMQURI + "/vhost#fragment"},
		{"invalid path escape", testRabbitMQURI + "/%zz"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			values := validRabbitMQEnv()
			values[EnvRabbitMQURI] = tc.uri
			_, err := loadRabbitMQ(lookup(values))
			if err == nil {
				t.Fatalf("invalid URI accepted: %s", tc.uri)
			}
			assertSecretSafeError(t, err, tc.uri)
		})
	}
}

func assertSecretSafeError(t *testing.T, err error, uri string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected secret-safe validation error")
	}
	message := err.Error()
	for _, secret := range []string{testRabbitMQUser, testRabbitMQPassword, uri} {
		if strings.Contains(message, secret) {
			t.Fatalf("validation error disclosed URI secret %q: %v", secret, err)
		}
	}
}

func TestRabbitMQValidateDoesNotDiscloseURI(t *testing.T) {
	rabbitmq := approvedRabbitMQ()
	rabbitmq.URI = testRabbitMQURI + "?heartbeat=30"
	err := rabbitmq.Validate()
	assertSecretSafeError(t, err, rabbitmq.URI)

	rabbitmq.URI = "amqp://" + testRabbitMQUser + ":" + testRabbitMQPassword + "@bad host/vhost"
	err = rabbitmq.Validate()
	assertSecretSafeError(t, err, rabbitmq.URI)
}

func TestRabbitMQValidateManagementURL(t *testing.T) {
	valid := []string{
		"http://rabbit.example.com:15672",
		"https://management.example.com/api",
		"https://[::1]:15672/",
	}
	for _, managementURL := range valid {
		t.Run("valid "+managementURL, func(t *testing.T) {
			rabbitmq := approvedRabbitMQ()
			rabbitmq.ManagementURL = managementURL
			if err := rabbitmq.Validate(); err != nil {
				t.Fatalf("valid management URL rejected: %v", err)
			}
		})
	}

	invalid := []struct {
		name string
		url  string
	}{
		{"empty", ""},
		{"relative", "rabbit.example.com:15672"},
		{"unsupported scheme", "ftp://rabbit.example.com"},
		{"opaque", "https:management.example.com"},
		{"missing host", "https://"},
		{"userinfo", "https://" + testRabbitMQUser + ":" + testRabbitMQPassword + "@rabbit.example.com"},
		{"query", "https://rabbit.example.com/api?token=secret"},
		{"empty query", "https://rabbit.example.com/api?"},
		{"fragment", "https://rabbit.example.com/api#fragment"},
		{"empty fragment", "https://rabbit.example.com/api#"},
		{"invalid escape", "https://rabbit.example.com/%zz"},
	}
	for _, tc := range invalid {
		t.Run("invalid "+tc.name, func(t *testing.T) {
			rabbitmq := approvedRabbitMQ()
			rabbitmq.ManagementURL = tc.url
			if err := rabbitmq.Validate(); err == nil {
				t.Fatalf("invalid management URL accepted: %q", tc.url)
			}
		})
	}
}

func TestRabbitMQValidateDurationsAndVHost(t *testing.T) {
	valid := approvedRabbitMQ()
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid RabbitMQ configuration rejected: %v", err)
	}
	cases := []struct {
		name     string
		mutate   func(*RabbitMQ)
		contains string
	}{
		{"empty vhost", func(r *RabbitMQ) { r.VHost = "" }, "vhost"},
		{"mismatched vhost", func(r *RabbitMQ) { r.VHost = "/other" }, "vhost"},
		{"connection name control", func(r *RabbitMQ) { r.ConnectionName = "worker\nname" }, "connection name"},
		{"connection name length", func(r *RabbitMQ) { r.ConnectionName = strings.Repeat("x", 256) }, "connection name"},
		{"heartbeat", func(r *RabbitMQ) { r.Heartbeat = 0 }, "heartbeat"},
		{"connect timeout", func(r *RabbitMQ) { r.ConnectTimeout = 0 }, "connect timeout"},
		{"minimum backoff", func(r *RabbitMQ) { r.ReconnectMinBackoff = 0 }, "minimum backoff"},
		{"maximum backoff", func(r *RabbitMQ) { r.ReconnectMaxBackoff = 0 }, "maximum backoff"},
		{"minimum above maximum", func(r *RabbitMQ) { r.ReconnectMinBackoff = 10*time.Second + time.Nanosecond }, "minimum backoff"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rabbitmq := valid
			tc.mutate(&rabbitmq)
			err := rabbitmq.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}

	boundary := valid
	boundary.ReconnectMinBackoff = boundary.ReconnectMaxBackoff
	if err := boundary.Validate(); err != nil {
		t.Fatalf("equal reconnect backoff bounds rejected: %v", err)
	}
}
