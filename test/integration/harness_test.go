//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/metamin44/hookflow/internal/app/delivery"
	"github.com/metamin44/hookflow/internal/app/outbox"
	"github.com/metamin44/hookflow/internal/app/worker"
	"github.com/metamin44/hookflow/internal/platform/config"
	"github.com/metamin44/hookflow/internal/platform/database"
	"github.com/metamin44/hookflow/internal/platform/id"
	"github.com/metamin44/hookflow/internal/platform/kek"
	"github.com/metamin44/hookflow/internal/platform/messaging"
	"github.com/metamin44/hookflow/internal/platform/observability"
	"github.com/metamin44/hookflow/internal/platform/secrets"
	"github.com/metamin44/hookflow/internal/platform/signing"
)

const queryTimeout = 5 * time.Second

type fixture struct {
	pool                   *pgxpool.Pool
	rabbit                 config.RabbitMQ
	conn                   *amqp.Connection
	publishCh              *amqp.Channel
	inspectCh              *amqp.Channel
	confirms               chan amqp.Confirmation
	returns                chan amqp.Return
	management             *managementClient
	logger                 *slog.Logger
	metrics                *observability.Metrics
	provider               *kek.MemoryProvider
	orgID                  string
	endpointID             string
	consumerConnectionName string

	bodiesMu sync.Mutex
	bodies   map[string]struct{}
}

type seededDelivery struct {
	id      string
	eventID string
}

type deliverySeed struct {
	state          string
	dispatchSeq    int
	attemptCount   int
	nextAttemptAt  time.Time
	eventReceived  time.Time
	eventExpiresAt time.Time
	completedAt    time.Time
	url            string
	headers        string
	secret         string
	payload        string
	eventType      string
	leaseToken     string
	leaseExpiresAt time.Time
	// endpointID overrides the fixture's primary endpoint, letting tests seed a
	// delivery whose stored endpoint differs from the one used for sealing.
	endpointID string
}

type deliveryRecord struct {
	state              string
	dispatchSeq        int
	attemptCount       int
	nextAttemptAt      pgtype.Timestamptz
	completedAt        pgtype.Timestamptz
	lastAttemptAt      pgtype.Timestamptz
	leaseToken         string
	leaseExpiresAt     pgtype.Timestamptz
	lastResponseStatus pgtype.Int4
	lastError          pgtype.Text
}

type outboxRecord struct {
	id          string
	sequence    int
	status      string
	attempts    int
	availableAt time.Time
	publishedAt pgtype.Timestamptz
	payload     string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	cfg, err := config.Load()
	if err != nil {
		t.Skipf("integration database configuration unavailable: %v", err)
	}
	rabbit, err := config.LoadRabbitMQ()
	if err != nil {
		t.Skipf("integration RabbitMQ configuration unavailable: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	pool, err := database.NewPool(ctx, cfg.Database)
	cancel()
	if err != nil {
		t.Fatalf("connect PostgreSQL: %v", err)
	}
	t.Cleanup(pool.Close)

	// Publisher claiming and Recovery are global. Skip rather than touch any
	// pre-existing work. The suite is explicitly intended to run with -p 1.
	var deliveries, outboxRows int
	if err := pool.Raw().QueryRow(context.Background(),
		`SELECT (SELECT count(*) FROM deliveries), (SELECT count(*) FROM outbox_messages)`,
	).Scan(&deliveries, &outboxRows); err != nil {
		t.Fatalf("check integration database isolation: %v", err)
	}
	if deliveries != 0 || outboxRows != 0 {
		t.Skipf("integration tests require an isolated database; found %d deliveries and %d outbox rows", deliveries, outboxRows)
	}

	rabbit.ConnectionName = "hookflow-integration-fixture-" + mustID(t)
	ctx, cancel = context.WithTimeout(context.Background(), queryTimeout)
	conn, err := messaging.ConnectAndEnsure(ctx, rabbit)
	cancel()
	if err != nil {
		t.Fatalf("connect RabbitMQ and verify HookFlow topology: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	publishCh, err := conn.Channel()
	if err != nil {
		t.Fatalf("open integration publish channel: %v", err)
	}
	if err := publishCh.Confirm(false); err != nil {
		t.Fatalf("enable integration publisher confirms: %v", err)
	}
	inspectCh, err := conn.Channel()
	if err != nil {
		t.Fatalf("open integration inspection channel: %v", err)
	}

	management, err := newManagementClient(rabbit)
	if err != nil {
		t.Fatalf("construct RabbitMQ management client: %v", err)
	}
	// queue.declare-ok reports current ready messages and consumers directly from
	// the broker. Management counters are sampled and can otherwise make the next
	// test skip after a preceding test has already ACKed its final pointer.
	queue, err := inspectCh.QueueDeclarePassive(messaging.DispatchQueue, false, false, false, false, nil)
	if err != nil {
		t.Fatalf("inspect shared dispatch queue: %v", err)
	}
	if queue.Consumers != 0 || queue.Messages != 0 {
		t.Skipf("integration tests require an idle dispatch queue; consumers=%d ready=%d",
			queue.Consumers, queue.Messages)
	}
	eventually(t, 8*time.Second, "dispatch queue unacknowledged telemetry to settle", func() error {
		info, err := management.queue(context.Background(), messaging.DispatchQueue)
		if err != nil {
			return err
		}
		if info.MessagesUnacknowledged != 0 {
			return fmt.Errorf("dispatch queue still reports %d unacknowledged messages", info.MessagesUnacknowledged)
		}
		return nil
	})

	provider, err := kek.NewMemoryProvider("kek-1", map[string][]byte{
		"kek-1": bytes.Repeat([]byte{0x5a}, secrets.DEKSize),
	}, nil)
	if err != nil {
		t.Fatalf("create integration wrapping provider: %v", err)
	}

	f := &fixture{
		pool:       pool.Raw(),
		rabbit:     rabbit,
		conn:       conn,
		publishCh:  publishCh,
		inspectCh:  inspectCh,
		confirms:   make(chan amqp.Confirmation, 32),
		returns:    make(chan amqp.Return, 32),
		management: management,
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		metrics:    observability.New(),
		provider:   provider,
		bodies:     make(map[string]struct{}),
		orgID:      mustID(t),
		endpointID: mustID(t),
	}
	publishCh.NotifyPublish(f.confirms)
	publishCh.NotifyReturn(f.returns)

	if _, err := f.pool.Exec(context.Background(),
		`INSERT INTO organizations (id, name, status) VALUES ($1::uuid, $2, 'active')`,
		f.orgID, "integration-"+f.orgID,
	); err != nil {
		t.Fatalf("seed organization: %v", err)
	}
	t.Cleanup(func() { f.cleanupRows(t) })
	if _, err := f.pool.Exec(context.Background(),
		`INSERT INTO endpoints (id, organization_id, name, url, secret_encrypted, status)
		 VALUES ($1::uuid, $2::uuid, $3, 'https://example.test/hook', '', 'active')`,
		f.endpointID, f.orgID, "endpoint-"+f.endpointID,
	); err != nil {
		t.Fatalf("seed unsigned endpoint: %v", err)
	}
	t.Cleanup(func() { f.drainOwnMessages(t) })
	return f
}

func (f *fixture) cleanupRows(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	statements := []string{
		`DELETE FROM delivery_attempts WHERE organization_id = $1::uuid`,
		`DELETE FROM outbox_messages WHERE organization_id = $1::uuid`,
		`DELETE FROM deliveries WHERE organization_id = $1::uuid`,
		`DELETE FROM events WHERE organization_id = $1::uuid`,
		`DELETE FROM endpoints WHERE organization_id = $1::uuid`,
		`DELETE FROM organizations WHERE id = $1::uuid`,
	}
	for _, statement := range statements {
		if _, err := f.pool.Exec(ctx, statement, f.orgID); err != nil {
			t.Errorf("cleanup own integration rows: %v", err)
			return
		}
	}
}

func (f *fixture) drainOwnMessages(t *testing.T) {
	// A failed assertion can leave an inspected pointer unacknowledged. Wait for
	// test consumers to disappear, then recycle only our inspection channel so
	// RabbitMQ requeues that pointer before the ownership check below.
	eventually(t, 12*time.Second, "test consumers to leave the dispatch queue", func() error {
		info := f.queueInfo(t, messaging.DispatchQueue)
		if info.Consumers != 0 {
			return fmt.Errorf("dispatch queue still has %d consumers", info.Consumers)
		}
		return nil
	})
	if info := f.queueInfo(t, messaging.DispatchQueue); info.MessagesUnacknowledged != 0 {
		_ = f.inspectCh.Close()
		channel, err := f.conn.Channel()
		if err != nil {
			t.Errorf("reopen integration inspection channel: %v", err)
			return
		}
		f.inspectCh = channel
		eventually(t, 12*time.Second, "inspected pointers to requeue", func() error {
			info := f.queueInfo(t, messaging.DispatchQueue)
			if info.MessagesUnacknowledged != 0 {
				return &queueStateError{info: info}
			}
			return nil
		})
	}
	for {
		message, ok, err := f.inspectCh.Get(messaging.DispatchQueue, false)
		if err != nil {
			t.Errorf("inspect dispatch queue during cleanup: %v", err)
			return
		}
		if !ok {
			return
		}
		if !f.ownsBody(message.Body) {
			t.Errorf("dispatch queue contained a message not owned by this test; leaving it unsettled")
			return
		}
		if err := f.inspectCh.Ack(message.DeliveryTag, false); err != nil {
			t.Errorf("acknowledge own leftover message: %v", err)
			return
		}
	}
}

func (f *fixture) seedDelivery(t *testing.T, spec deliverySeed) seededDelivery {
	t.Helper()
	now := time.Now()
	if spec.state == "" {
		spec.state = "pending"
	}
	if spec.dispatchSeq == 0 {
		spec.dispatchSeq = 1
	}
	if spec.url == "" {
		spec.url = "https://example.test/hook"
	}
	if spec.headers == "" {
		spec.headers = `{}`
	}
	if spec.payload == "" {
		spec.payload = `{"source":"integration"}`
	}
	if spec.eventType == "" {
		spec.eventType = "test.integration"
	}
	if spec.eventReceived.IsZero() {
		spec.eventReceived = now.Add(-time.Hour)
	}
	if spec.eventExpiresAt.IsZero() {
		spec.eventExpiresAt = now.Add(time.Hour)
	}
	live := spec.state == "pending" || spec.state == "retrying"
	if live && spec.nextAttemptAt.IsZero() {
		spec.nextAttemptAt = now.Add(-time.Minute)
	}
	if !live && spec.completedAt.IsZero() {
		spec.completedAt = now
	}

	var nextAttemptAt, completedAt, leaseToken, leaseExpiresAt any
	if live {
		nextAttemptAt = spec.nextAttemptAt
	}
	if !spec.completedAt.IsZero() {
		completedAt = spec.completedAt
	}
	if spec.leaseToken != "" {
		leaseToken = spec.leaseToken
		leaseExpiresAt = spec.leaseExpiresAt
		if leaseExpiresAt == nil && spec.leaseExpiresAt.IsZero() {
			leaseExpiresAt = now.Add(time.Hour)
		}
	}

	deliveryID, eventID := mustID(t), mustID(t)
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO events
		     (id, organization_id, type, payload, destination_count, received_at, expires_at)
		 VALUES ($1::uuid, $2::uuid, $3, $4::jsonb, 1, $5::timestamptz, $6::timestamptz)`,
		eventID, f.orgID, spec.eventType, spec.payload, spec.eventReceived, spec.eventExpiresAt,
	); err != nil {
		t.Fatalf("seed event: %v", err)
	}
	endpointID := f.endpointID
	if spec.endpointID != "" {
		endpointID = spec.endpointID
	}
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO deliveries
		     (id, organization_id, event_id, endpoint_id, url, http_headers, secret_encrypted,
		      state, dispatch_seq, attempt_count, next_attempt_at, created_at, updated_at,
		      completed_at, dispatch_lease_token, dispatch_lease_expires_at)
		 VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5, $6::jsonb, $7,
		         $8, $9, $10, $11::timestamptz, $12::timestamptz, $12::timestamptz,
		         $13::timestamptz, $14::uuid, $15::timestamptz)`,
		deliveryID, f.orgID, eventID, endpointID, spec.url, spec.headers, spec.secret,
		spec.state, spec.dispatchSeq, spec.attemptCount, nextAttemptAt, now,
		completedAt, leaseToken, leaseExpiresAt,
	); err != nil {
		t.Fatalf("seed delivery: %v", err)
	}
	return seededDelivery{id: deliveryID, eventID: eventID}
}

// seedEndpoint inserts an additional endpoint owned by the fixture organization.
// It is used only to prove endpoint identity binding; cleanup cascades with the
// fixture organization.
func (f *fixture) seedEndpoint(t *testing.T) string {
	t.Helper()
	endpointID := mustID(t)
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO endpoints (id, organization_id, name, url, secret_encrypted, status)
		 VALUES ($1::uuid, $2::uuid, $3, 'https://example.test/other', '', 'active')`,
		endpointID, f.orgID, "endpoint-"+endpointID,
	); err != nil {
		t.Fatalf("seed secondary endpoint: %v", err)
	}
	return endpointID
}

func (f *fixture) seedOutbox(t *testing.T, deliveryID string, sequence int, status string, availableAt, publishedAt time.Time) string {
	t.Helper()
	outboxID := mustID(t)
	if availableAt.IsZero() {
		availableAt = time.Now().Add(-time.Minute)
	}
	if status == "published" && publishedAt.IsZero() {
		publishedAt = time.Now().Add(-2 * time.Minute)
	}
	payload, err := messaging.BuildOutboxPayload(outboxID, f.orgID, deliveryID, sequence)
	if err != nil {
		t.Fatalf("build outbox payload: %v", err)
	}
	kind := "delivery.dispatch"
	if sequence > 1 {
		kind = "delivery.retry"
	}
	var published any
	if status == "published" {
		published = publishedAt
	}
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO outbox_messages
		     (id, organization_id, delivery_id, sequence, kind, topic, payload, headers,
		      status, attempts, available_at, published_at, created_at, updated_at)
		 VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7::jsonb, $8::jsonb,
		         $9, 0, $10::timestamptz, $11::timestamptz, $10::timestamptz, $10::timestamptz)`,
		outboxID, f.orgID, deliveryID, sequence, kind, messaging.DispatchRoutingKey,
		payload, fmt.Sprintf(`{"x-hookflow-schema-version":"%d"}`, messaging.SchemaVersion),
		status, availableAt, published,
	); err != nil {
		t.Fatalf("seed outbox row: %v", err)
	}
	return outboxID
}

func (f *fixture) publishPointer(t *testing.T, deliveryID, outboxID string, sequence int) {
	t.Helper()
	if err := f.publish(context.Background(), messaging.Envelope{
		SchemaVersion: messaging.SchemaVersion, OrganizationID: f.orgID,
		DeliveryID: deliveryID, DispatchSeq: sequence, OutboxID: outboxID,
	}); err != nil {
		t.Fatalf("publish pointer: %v", err)
	}
}

func (f *fixture) publish(ctx context.Context, envelope messaging.Envelope) error {
	publishing, err := messaging.EncodePublishing(envelope)
	if err != nil {
		return err
	}
	return f.publishPublishing(ctx, publishing)
}

func (f *fixture) publishPublishing(ctx context.Context, publishing amqp.Publishing) error {
	f.bodiesMu.Lock()
	f.bodies[string(publishing.Body)] = struct{}{}
	f.bodiesMu.Unlock()
	confirmation, err := f.publishCh.PublishWithDeferredConfirmWithContext(
		ctx, messaging.DispatchExchange, messaging.DispatchRoutingKey, true, false, publishing,
	)
	if err != nil || confirmation == nil {
		return fmt.Errorf("deferred publish: confirmation=%v error=%w", confirmation, err)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case value := <-f.confirms:
		if value.DeliveryTag != confirmation.DeliveryTag || !value.Ack {
			return fmt.Errorf("publisher confirmation = %+v, wanted ack for tag %d", value, confirmation.DeliveryTag)
		}
	case value := <-f.returns:
		return fmt.Errorf("routable pointer was returned: %+v", value)
	}
	select {
	case value := <-f.returns:
		return fmt.Errorf("routable pointer was returned after confirmation: %+v", value)
	default:
		return nil
	}
}

func (f *fixture) publishMalformed(t *testing.T, body string) {
	t.Helper()
	publishing := amqp.Publishing{
		ContentType: amqp.MimeApplicationJSON, DeliveryMode: amqp.Persistent,
		MessageId: mustID(t), Body: []byte(body),
	}
	if err := f.publishPublishing(context.Background(), publishing); err != nil {
		t.Fatalf("publish malformed pointer: %v", err)
	}
}

func (f *fixture) ownsBody(body []byte) bool {
	f.bodiesMu.Lock()
	_, ok := f.bodies[string(body)]
	f.bodiesMu.Unlock()
	if ok {
		return true
	}
	// Publisher Session tests bypass publishPublishing. Recognize their valid
	// pointers by the fixture's random organization ID so UNKNOWN-outcome cleanup
	// can still remove an accepted duplicate without touching another test's work.
	var envelope messaging.Envelope
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.OrganizationID != f.orgID {
		return false
	}
	f.bodiesMu.Lock()
	f.bodies[string(body)] = struct{}{}
	f.bodiesMu.Unlock()
	return true
}

func (f *fixture) claimOne(t *testing.T) outbox.Claim {
	t.Helper()
	store := outbox.NewStore(f.pool, f.logger)
	claims, err := store.Claim(context.Background(), outbox.ClaimOptions{
		BatchSize: 1, MaxClaims: 3, LeaseDuration: 2 * time.Second,
		ProcessID: "integration-" + mustID(t), NewToken: outbox.RandomToken,
	})
	if err != nil {
		t.Fatalf("claim outbox row: %v", err)
	}
	if len(claims) != 1 {
		t.Fatalf("claim count = %d, want 1", len(claims))
	}
	return claims[0]
}

func (f *fixture) readDelivery(t *testing.T, deliveryID string) deliveryRecord {
	t.Helper()
	var row deliveryRecord
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	err := f.pool.QueryRow(ctx,
		`SELECT state, dispatch_seq, attempt_count, next_attempt_at, completed_at, last_attempt_at,
		        COALESCE(dispatch_lease_token::text, ''), dispatch_lease_expires_at,
		        last_response_status, last_error
		   FROM deliveries WHERE id = $1::uuid`, deliveryID,
	).Scan(&row.state, &row.dispatchSeq, &row.attemptCount, &row.nextAttemptAt,
		&row.completedAt, &row.lastAttemptAt, &row.leaseToken, &row.leaseExpiresAt,
		&row.lastResponseStatus, &row.lastError)
	if err != nil {
		t.Fatalf("read delivery: %v", err)
	}
	return row
}

func (f *fixture) readOutboxes(t *testing.T, deliveryID string) []outboxRecord {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	rows, err := f.pool.Query(ctx,
		`SELECT id::text, sequence, status, attempts, available_at, published_at, payload::text
		   FROM outbox_messages WHERE delivery_id = $1::uuid ORDER BY sequence, id`, deliveryID,
	)
	if err != nil {
		t.Fatalf("query outbox rows: %v", err)
	}
	defer rows.Close()
	var result []outboxRecord
	for rows.Next() {
		var row outboxRecord
		if err := rows.Scan(&row.id, &row.sequence, &row.status, &row.attempts,
			&row.availableAt, &row.publishedAt, &row.payload); err != nil {
			t.Fatalf("scan outbox row: %v", err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read outbox rows: %v", err)
	}
	return result
}

func (f *fixture) attemptCount(t *testing.T, deliveryID string) int {
	t.Helper()
	var count int
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM delivery_attempts WHERE delivery_id = $1::uuid`, deliveryID,
	).Scan(&count); err != nil {
		t.Fatalf("count delivery attempts: %v", err)
	}
	return count
}

func (f *fixture) attemptOutcome(t *testing.T, deliveryID string) string {
	t.Helper()
	var outcome string
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	if err := f.pool.QueryRow(ctx,
		`SELECT outcome FROM delivery_attempts WHERE delivery_id = $1::uuid`, deliveryID,
	).Scan(&outcome); err != nil {
		t.Fatalf("read delivery attempt: %v", err)
	}
	return outcome
}

func (f *fixture) attemptHeaders(t *testing.T, deliveryID string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	rows, err := f.pool.Query(ctx,
		`SELECT request_headers::text FROM delivery_attempts
		  WHERE delivery_id = $1::uuid ORDER BY attempt_number`, deliveryID,
	)
	if err != nil {
		t.Fatalf("query attempt request headers: %v", err)
	}
	defer rows.Close()
	var headers []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatalf("scan attempt request headers: %v", err)
		}
		headers = append(headers, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read attempt request headers: %v", err)
	}
	return headers
}

func (f *fixture) expireWorkerLease(t *testing.T, deliveryID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	if _, err := f.pool.Exec(ctx,
		`UPDATE deliveries SET dispatch_lease_expires_at = clock_timestamp() - interval '1 second'
		  WHERE id = $1::uuid`, deliveryID,
	); err != nil {
		t.Fatalf("expire own worker lease: %v", err)
	}
}

func (f *fixture) agePublisherLease(t *testing.T, outboxID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	if _, err := f.pool.Exec(ctx,
		`UPDATE outbox_messages SET locked_at = clock_timestamp() - interval '1 second',
		  available_at = clock_timestamp() - interval '1 second'
		  WHERE id = $1::uuid`, outboxID,
	); err != nil {
		t.Fatalf("age own publisher lease: %v", err)
	}
}

func (f *fixture) queueInfo(t *testing.T, name string) queueInfo {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	info, err := f.management.queue(ctx, name)
	if err != nil {
		t.Fatalf("read queue %s telemetry: %v", name, err)
	}
	return info
}

func (f *fixture) metricValue(t *testing.T, name string) int64 {
	t.Helper()
	recorder := httptest.NewRecorder()
	f.metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, line := range strings.Split(recorder.Body.String(), "\n") {
		if strings.HasPrefix(line, name+" ") {
			value, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, name+" ")), 10, 64)
			if err != nil {
				t.Fatalf("parse metric %s: %v", name, err)
			}
			return value
		}
	}
	return 0
}

func testWorkerConfig(prefetch int) config.DeliveryWorker {
	return config.DeliveryWorker{
		LeaseDuration: 3 * time.Second, HTTPDeadline: 500 * time.Millisecond,
		HandshakeBudget: 100 * time.Millisecond, PostProcessBudget: 100 * time.Millisecond,
		ShutdownReserve: 100 * time.Millisecond, SecretResolutionBudget: 100 * time.Millisecond,
		Concurrency: prefetch, Prefetch: prefetch,
		PoolSize: prefetch, MaxAttempts: 3, RetryHorizon: time.Minute,
		DecryptFailurePacing: 10 * time.Millisecond, ResponseBodyLimit: 4096,
	}
}

// testSecurityConfig returns a development configuration whose unsigned gate is
// enabled so fixtures with an empty stored secret remain deliverable. The
// keyring path is deliberately outside the repository; only the in-memory test
// provider is used.
func testSecurityConfig() config.DeliverySecurity {
	return config.DeliverySecurity{
		Mode:                  config.ModeDevelopment,
		AllowUnsignedDelivery: true,
		Provider:              kek.ProviderLocalFile,
		KeyringPath:           "outside-repository/integration-keyring.json",
		CurrentKID:            "kek-1",
		AllowedKIDs:           []string{"kek-1"},
		AllowedProviders:      []string{kek.ProviderLocalFile},
	}
}

// sealSecret builds a real stored envelope for the fixture endpoint so a worker
// resolves it through the same provider the runtime holds.
func (f *fixture) sealSecret(t *testing.T, plaintext []byte) string {
	t.Helper()
	return f.sealSecretFor(t, f.orgID, f.endpointID, plaintext)
}

// sealSecretFor binds the envelope to an explicit organization and endpoint.
func (f *fixture) sealSecretFor(t *testing.T, organizationID, endpointID string, plaintext []byte) string {
	t.Helper()
	return f.buildSecretEnvelope(t, organizationID, endpointID, organizationID, endpointID,
		plaintext, secrets.ProviderLocalFile, "", nil)
}

// buildSecretEnvelope seals a payload under the wrap identity (provider KEK
// context) and the payload AAD identity, which are deliberately separable so
// tests can prove both bindings. An empty kid defaults to the wrapped kid.
func (f *fixture) buildSecretEnvelope(t *testing.T, wrapOrganizationID, wrapEndpointID, aadOrganizationID, aadEndpointID string, plaintext []byte, providerTag byte, kid string, mutate func(*secrets.Envelope)) string {
	t.Helper()
	wrapOrganization, err := secrets.ParseUUID(wrapOrganizationID)
	if err != nil {
		t.Fatalf("parse secret wrap organization id: %v", err)
	}
	wrapEndpoint, err := secrets.ParseUUID(wrapEndpointID)
	if err != nil {
		t.Fatalf("parse secret wrap endpoint id: %v", err)
	}
	aadOrganization, err := secrets.ParseUUID(aadOrganizationID)
	if err != nil {
		t.Fatalf("parse secret AAD organization id: %v", err)
	}
	aadEndpoint, err := secrets.ParseUUID(aadEndpointID)
	if err != nil {
		t.Fatalf("parse secret AAD endpoint id: %v", err)
	}
	dek := bytes.Repeat([]byte{0x2a}, secrets.DEKSize)
	nonce := bytes.Repeat([]byte{0x11}, secrets.NonceSize)
	aad, err := secrets.BuildAAD(secrets.AlgorithmAES256GCM, secrets.PurposeWebhookSecret, nonce, aadOrganization, aadEndpoint)
	if err != nil {
		t.Fatalf("build secret AAD: %v", err)
	}
	ciphertext, tag, err := secrets.Seal(dek, nonce, plaintext, aad[:])
	if err != nil {
		t.Fatalf("seal fixture secret: %v", err)
	}
	wrapped, err := f.provider.Wrap(context.Background(), dek,
		kek.Context{OrganizationID: wrapOrganization, EndpointID: wrapEndpoint})
	if err != nil {
		t.Fatalf("wrap fixture data key: %v", err)
	}
	if kid == "" {
		kid = wrapped.KID
	}
	envelope := secrets.Envelope{
		Version:    secrets.EnvelopeVersion,
		Algorithm:  secrets.AlgorithmAES256GCM,
		Purpose:    secrets.PurposeWebhookSecret,
		KID:        kid,
		Provider:   providerTag,
		WrappedDEK: wrapped.Blob,
		Nonce:      nonce,
		Ciphertext: ciphertext,
		Tag:        tag,
	}
	if mutate != nil {
		mutate(&envelope)
	}
	encoded, err := envelope.Encode()
	if err != nil {
		t.Fatalf("encode fixture secret envelope: %v", err)
	}
	return encoded
}

// deliveryTestURL is a public-shaped destination used with pinnedClient. The
// Worker's destination policy check runs before every attempt and rejects
// loopback literals, so loopback fixtures must present a public URL while the
// injected client dials the local TLS listener.
const deliveryTestURL = "https://example.com/hook"

// pinnedClient returns the httptest TLS server client with its dial pinned to
// the listener. Production never constructs this: the real transport resolves
// and validates the destination itself.
func pinnedClient(t *testing.T, server *httptest.Server) *http.Client {
	t.Helper()
	transport, ok := server.Client().Transport.(*http.Transport)
	if !ok {
		t.Fatalf("httptest client transport = %T", server.Client().Transport)
	}
	cloned := transport.Clone()
	address := server.Listener.Addr().String()
	cloned.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	return &http.Client{Transport: cloned}
}

func (f *fixture) newWorker(t *testing.T, cfg config.DeliveryWorker, client *http.Client) (*worker.Runtime, *worker.Executor, *worker.RetryPolicy) {
	t.Helper()
	return f.newWorkerWith(t, cfg, client, testSecurityConfig(), f.provider, signing.New())
}

// newWorkerWith is the explicit-seam constructor used by the audit suites. A nil
// signer or provider falls back to the fixture defaults so existing tests keep
// their behavior.
func (f *fixture) newWorkerWith(t *testing.T, cfg config.DeliveryWorker, client *http.Client, security config.DeliverySecurity, provider kek.Provider, signer worker.RequestSigner) (*worker.Runtime, *worker.Executor, *worker.RetryPolicy) {
	t.Helper()
	if provider == nil {
		provider = f.provider
	}
	if signer == nil {
		signer = signing.New()
	}
	store, err := delivery.NewStore(f.pool, delivery.Options{
		NewID: id.NewV7, BuildRetryMessage: delivery.BuildRetryMessage, Logger: f.logger,
	})
	if err != nil {
		t.Fatalf("create delivery store: %v", err)
	}
	executor, err := worker.NewExecutor(worker.HTTPOptions{
		Deadline: cfg.HTTPDeadline, HandshakeBudget: cfg.HandshakeBudget,
		PostProcessBudget: cfg.PostProcessBudget, ShutdownReserve: cfg.ShutdownReserve,
		ResponseBodyLimit: cfg.ResponseBodyLimit, Client: client,
	}, signer, f.logger)
	if err != nil {
		t.Fatalf("create HTTP executor: %v", err)
	}
	retry, err := worker.NewRetryPolicy(cfg.MaxAttempts, cfg.RetryHorizon)
	if err != nil {
		t.Fatalf("create retry policy: %v", err)
	}
	runtime, err := worker.NewRuntime(cfg, security, provider, store, executor,
		retry, delivery.RandomToken, f.logger, f.metrics)
	if err != nil {
		t.Fatalf("create worker runtime: %v", err)
	}
	return runtime, executor, retry
}

func (f *fixture) newConsumer(t *testing.T, cfg config.DeliveryWorker, processor worker.Processor) *worker.Consumer {
	t.Helper()
	rabbit := f.rabbit
	rabbit.ConnectionName = "hookflow-integration-worker-" + mustID(t)
	f.consumerConnectionName = rabbit.ConnectionName
	rabbit.Heartbeat = 2 * time.Second
	rabbit.ConnectTimeout = time.Second
	rabbit.ReconnectMinBackoff = 10 * time.Millisecond
	rabbit.ReconnectMaxBackoff = 20 * time.Millisecond
	consumer, err := worker.NewConsumer(rabbit, cfg.Prefetch, cfg.LeaseDuration-cfg.ShutdownReserve,
		processor, f.logger, f.metrics)
	if err != nil {
		t.Fatalf("create worker consumer: %v", err)
	}
	return consumer
}

type consumerRun struct {
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

func startConsumer(t *testing.T, consumer *worker.Consumer) *consumerRun {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	run := &consumerRun{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(run.done)
		run.err = consumer.Run(ctx)
	}()
	t.Cleanup(func() { run.stop(t) })
	return run
}

func (r *consumerRun) stop(t *testing.T) {
	t.Helper()
	r.cancel()
	select {
	case <-r.done:
		if r.err != nil {
			t.Errorf("consumer.Run() error: %v", r.err)
		}
	case <-time.After(4 * time.Second):
		t.Error("consumer did not stop within shutdown deadline")
	}
}

func eventually(t *testing.T, timeout time.Duration, description string, condition func() error) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var last error
	for {
		if last = condition(); last == nil {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s: %v", description, last)
		case <-ticker.C:
		}
	}
}

func mustID(t *testing.T) string {
	t.Helper()
	value, err := id.NewV7()
	if err != nil {
		t.Fatalf("generate UUIDv7: %v", err)
	}
	return value
}

type managementClient struct {
	baseURL  string
	username string
	password string
	vhost    string
	client   *http.Client
}

func newManagementClient(cfg config.RabbitMQ) (*managementClient, error) {
	uri, err := amqp.ParseURI(cfg.URI)
	if err != nil {
		return nil, err
	}
	return &managementClient{
		baseURL:  strings.TrimSuffix(strings.TrimRight(cfg.ManagementURL, "/"), "/api"),
		username: uri.Username, password: uri.Password, vhost: uri.Vhost,
		client: &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		}},
	}, nil
}

func (m *managementClient) request(ctx context.Context, method, path string, output any) (int, error) {
	request, err := http.NewRequestWithContext(ctx, method, m.baseURL+path, nil)
	if err != nil {
		return 0, err
	}
	request.SetBasicAuth(m.username, m.password)
	request.Header.Set("Accept", "application/json")
	response, err := m.client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64*1024))
		return response.StatusCode, nil
	}
	if output == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64*1024))
		return response.StatusCode, nil
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 2*1024*1024))
	decoder.UseNumber()
	if err := decoder.Decode(output); err != nil {
		return response.StatusCode, err
	}
	return response.StatusCode, nil
}

func (m *managementClient) queue(ctx context.Context, name string) (queueInfo, error) {
	var info queueInfo
	path := "/api/queues/" + url.PathEscape(m.vhost) + "/" + url.PathEscape(name)
	status, err := m.request(ctx, http.MethodGet, path, &info)
	if err != nil {
		return queueInfo{}, err
	}
	if status != http.StatusOK {
		return queueInfo{}, fmt.Errorf("queue lookup status %d", status)
	}
	return info, nil
}

type queueInfo struct {
	Messages               int          `json:"messages"`
	MessagesReady          int          `json:"messages_ready"`
	MessagesUnacknowledged int          `json:"messages_unacknowledged"`
	Consumers              int          `json:"consumers"`
	MessageStats           messageStats `json:"message_stats"`
}

type messageStats map[string]int64

func (s *messageStats) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	stats := make(messageStats, len(raw))
	for name, value := range raw {
		var count int64
		if err := json.Unmarshal(value, &count); err == nil {
			stats[name] = count
		}
	}
	*s = stats
	return nil
}

type connectionInfo struct {
	Name             string `json:"name"`
	Channels         int    `json:"channels"`
	VHost            string `json:"vhost"`
	ClientProperties struct {
		ConnectionName string `json:"connection_name"`
	} `json:"client_properties"`
}

func (m *managementClient) connections(ctx context.Context) ([]connectionInfo, error) {
	var connections []connectionInfo
	status, err := m.request(ctx, http.MethodGet, "/api/connections", &connections)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("connection lookup status %d", status)
	}
	return connections, nil
}

func (f *fixture) waitOwnConnection(t *testing.T, connectionName string) string {
	t.Helper()
	var name string
	eventually(t, 12*time.Second, "identify the test-owned RabbitMQ connection", func() error {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		connections, err := f.management.connections(ctx)
		if err != nil {
			return err
		}
		for _, connection := range connections {
			if connection.ClientProperties.ConnectionName == connectionName {
				name = connection.Name
				return nil
			}
		}
		visible := make([]string, 0, len(connections))
		for _, connection := range connections {
			visible = append(visible, connection.Name+"/"+connection.ClientProperties.ConnectionName)
		}
		return fmt.Errorf("test connection %q is not visible yet; visible=%v", connectionName, visible)
	})
	t.Cleanup(func() { f.closeConnection(t, name) })
	return name
}

func (f *fixture) closeConnection(t *testing.T, name string) {
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	status, err := f.management.request(ctx, http.MethodDelete, "/api/connections/"+url.PathEscape(name), nil)
	if err != nil || (status != http.StatusNoContent && status != http.StatusNotFound) {
		t.Errorf("close test-owned RabbitMQ connection: status=%d error=%v", status, err)
	}
}
