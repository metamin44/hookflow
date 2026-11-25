package publisher

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/metamin44/hookflow/internal/app/outbox"
	"github.com/metamin44/hookflow/internal/platform/config"
)

type fakeStore struct {
	claims     []outbox.Claim
	claimErr   error
	published  []string
	failed     []string
	markErr    error
	reapResult outbox.ReapResult
}

func (s *fakeStore) Claim(context.Context, outbox.ClaimOptions) ([]outbox.Claim, error) {
	return s.claims, s.claimErr
}
func (s *fakeStore) MarkPublished(_ context.Context, claim outbox.Claim) (bool, error) {
	s.published = append(s.published, claim.MessageID)
	return s.markErr == nil, s.markErr
}
func (s *fakeStore) MarkFailed(_ context.Context, failure outbox.Failure) (outbox.FailureResult, error) {
	s.failed = append(s.failed, failure.Claim.MessageID)
	return outbox.FailureResult{Applied: true}, nil
}
func (s *fakeStore) ReapExpired(context.Context, outbox.ReapOptions) (outbox.ReapResult, error) {
	return s.reapResult, nil
}

type fakeBroker struct {
	result    PublishResult
	err       error
	published []string
	ensureErr error
}

func (b *fakeBroker) Ensure(context.Context) error { return b.ensureErr }
func (b *fakeBroker) Publish(_ context.Context, claim outbox.Claim) (PublishResult, error) {
	b.published = append(b.published, claim.MessageID)
	return b.result, b.err
}
func (b *fakeBroker) Close() {}

func testClaim(id string, count int) outbox.Claim {
	return outbox.Claim{
		MessageID: id, OrganizationID: "11111111-1111-4111-8111-111111111111",
		DeliveryID: "22222222-2222-4222-8222-222222222222", Sequence: 1,
		Kind: "delivery.dispatch", Topic: "delivery", Owner: "publisher-test",
		MaxClaims: 5, ClaimCount: count, LeaseToken: "33333333-3333-4333-8333-333333333333",
	}
}

func testRelay(t *testing.T, store *fakeStore, broker *fakeBroker) *Relay {
	t.Helper()
	relay, err := NewRelay(Options{
		Config: config.Publisher{BatchSize: 2, MaxClaims: 5, LeaseDuration: 30 * time.Second,
			PollInterval: 500 * time.Millisecond, ReapInterval: 10 * time.Second,
			BackoffBase: 2 * time.Second, BackoffCap: 60 * time.Second, BackoffMin: 100 * time.Millisecond, ConfirmTimeout: 5 * time.Second},
		RabbitMQ: config.RabbitMQ{URI: "amqp://user:password@localhost:5672/", ManagementURL: "http://localhost:15672", VHost: "/",
			Heartbeat: 10 * time.Second, ConnectTimeout: 5 * time.Second, ReconnectMinBackoff: 500 * time.Millisecond, ReconnectMaxBackoff: 10 * time.Second},
		ProcessID: "publisher-test", Store: store, Broker: broker, NewToken: outbox.RandomToken,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("NewRelay() error: %v", err)
	}
	return relay
}

func TestTickPublishesOnlyConfirmed(t *testing.T) {
	store := &fakeStore{claims: []outbox.Claim{testClaim("44444444-4444-4444-8444-444444444444", 5)}}
	broker := &fakeBroker{result: ResultConfirmed}
	relay := testRelay(t, store, broker)
	full, unknown := relay.tick(context.Background())
	if full || unknown || len(store.published) != 1 || len(store.failed) != 0 {
		t.Fatalf("unexpected tick: full=%t unknown=%t published=%v failed=%v", full, unknown, store.published, store.failed)
	}
}

func TestTickNeverMarksUnknownPublished(t *testing.T) {
	for _, result := range []PublishResult{ResultUnknown, ResultReturned, ResultNacked} {
		t.Run(string(result), func(t *testing.T) {
			store := &fakeStore{claims: []outbox.Claim{testClaim("44444444-4444-4444-8444-444444444444", 1)}}
			broker := &fakeBroker{result: result, err: errors.New("transport lost")}
			relay := testRelay(t, store, broker)
			_, unknown := relay.tick(context.Background())
			if !unknown || len(store.published) != 0 || len(store.failed) != 1 {
				t.Fatalf("unknown=%t published=%v failed=%v", unknown, store.published, store.failed)
			}
		})
	}
}

func TestTickCommitErrorDoesNotReportSuccess(t *testing.T) {
	store := &fakeStore{claims: []outbox.Claim{testClaim("44444444-4444-4444-8444-444444444444", 1)}, markErr: errors.New("commit lost")}
	broker := &fakeBroker{result: ResultConfirmed}
	relay := testRelay(t, store, broker)
	_, unknown := relay.tick(context.Background())
	if !unknown || len(store.failed) != 1 {
		t.Fatalf("unknown=%t failed=%v", unknown, store.failed)
	}
}

func TestBackoffBoundsAndOverflow(t *testing.T) {
	random := func(upper *big.Int) (*big.Int, error) { return new(big.Int).Sub(upper, big.NewInt(1)), nil }
	for count := 1; count <= 100; count++ {
		value, err := Backoff(count, 2*time.Second, 60*time.Second, 100*time.Millisecond, random)
		if err != nil {
			t.Fatalf("Backoff(%d): %v", count, err)
		}
		want := time.Duration(2) * time.Second
		if count >= 2 && count <= 5 {
			want = time.Duration(1<<(count-1)) * 2 * time.Second
		}
		if count >= 6 {
			want = 60 * time.Second
		}
		if value != want {
			t.Fatalf("Backoff(%d)=%s want %s", count, value, want)
		}
	}
	if _, err := Backoff(0, time.Second, time.Second, time.Millisecond, random); err == nil {
		t.Fatal("zero claim count accepted")
	}
}

func TestDecisionReturnWinsRegardlessOfObservationOrder(t *testing.T) {
	returnValue := amqpReturn("44444444-4444-4444-8444-444444444444")
	confirmation := amqpConfirmation(1, true)
	for _, returnFirst := range []bool{true, false} {
		decision := &publishDecision{messageID: returnValue.MessageId, sent: true, tag: 1}
		if returnFirst {
			if err := decision.observeReturn(returnValue); err != nil {
				t.Fatal(err)
			}
			if err := decision.observeConfirmation(confirmation); err != nil {
				t.Fatal(err)
			}
		} else {
			if err := decision.observeConfirmation(confirmation); err != nil {
				t.Fatal(err)
			}
			if err := decision.observeReturn(returnValue); err != nil {
				t.Fatal(err)
			}
		}
		result, err := decision.result()
		if err != nil || result != ResultReturned {
			t.Fatalf("returnFirst=%t result=%s err=%v", returnFirst, result, err)
		}
	}
}

func TestDecisionRejectsDuplicatesAndNegativeConfirm(t *testing.T) {
	decision := &publishDecision{messageID: "44444444-4444-4444-8444-444444444444", sent: true, tag: 1}
	if err := decision.observeConfirmation(amqpConfirmation(1, false)); err != nil {
		t.Fatal(err)
	}
	if result, err := decision.result(); err == nil || result != ResultUnknown {
		t.Fatalf("negative confirm result=%s err=%v", result, err)
	}
	decision = &publishDecision{messageID: "44444444-4444-4444-8444-444444444444", sent: true}
	if err := decision.observeReturn(amqpReturn(decision.messageID)); err != nil {
		t.Fatal(err)
	}
	if err := decision.observeReturn(amqpReturn(decision.messageID)); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate return error=%v", err)
	}
}
