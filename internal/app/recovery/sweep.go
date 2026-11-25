// Package recovery implements the approved database-side Recovery Sweep.
//
// It exposes one bounded invocation rather than a production scheduler loop:
// pass budgets, stale thresholds and message construction are explicit inputs
// because their operational values and broker topology are not approved.
package recovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/metamin44/hookflow/internal/platform/config"
)

// Beginner is satisfied by *pgxpool.Pool and by pgx.Tx.
type Beginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

// PassBudget bounds one duty's candidate attempts and wall-clock allocation.
type PassBudget struct {
	MaxCandidates int
	MaxDuration   time.Duration
}

// Budget gives every duty its own reserved allowance. The sum of pass
// durations must not exceed Total.
type Budget struct {
	Total  time.Duration
	Expire PassBudget
	Repair PassBudget
	ReArm  PassBudget
	Stale  PassBudget
}

// Validate checks the explicit capacity contract.
func (b Budget) Validate() error {
	if b.Total <= 0 {
		return errors.New("recovery total deadline must be positive")
	}
	passes := []struct {
		name string
		p    PassBudget
	}{
		{"expire", b.Expire},
		{"repair", b.Repair},
		{"re-arm", b.ReArm},
		{"stale", b.Stale},
	}
	for _, pass := range passes {
		if pass.p.MaxCandidates <= 0 {
			return fmt.Errorf("recovery %s pass candidate allowance must be positive", pass.name)
		}
		if pass.p.MaxDuration <= 0 {
			return fmt.Errorf("recovery %s pass duration must be positive", pass.name)
		}
	}
	if err := config.ValidateDurationBudget(b.Total,
		b.Expire.MaxDuration, b.Repair.MaxDuration, b.ReArm.MaxDuration, b.Stale.MaxDuration,
	); err != nil {
		return fmt.Errorf("recovery budget: %w", err)
	}
	return nil
}

// Delivery is the current-generation delivery input used to build an outbox
// message. The builder owns topic and payload because U21 topology is open.
type Delivery struct {
	OrganizationID string
	DeliveryID     string
	EventID        string
	DispatchSeq    int
	NextAttemptAt  time.Time
	State          string
	CreatedAt      time.Time
}

// Message is the explicit outbox row a builder asks the Sweep to insert.
type Message struct {
	ID       string
	Sequence int
	Kind     string
	Topic    string
	Payload  string
	Headers  string
}

// MessageBuilder constructs the approved small dispatch intent. It must be
// supplied by the caller; this package does not invent broker topology.
type MessageBuilder func(Delivery) (Message, error)

// Options are the explicit unresolved Recovery Sweep parameters.
type Options struct {
	Budget              Budget
	StalePublishedAfter time.Duration
	BuildMessage        MessageBuilder
	Logger              *slog.Logger
}

// PassReport reports one bounded duty.
type PassReport struct {
	Pass               string
	Selected           int
	Applied            int
	Deferred           int
	Ineligible         int
	OldestCandidateAge time.Duration
	Duration           time.Duration
}

// Report reports one bounded invocation.
type Report struct {
	Expire   PassReport
	Repair   PassReport
	ReArm    PassReport
	Stale    PassReport
	Duration time.Duration
}

// Sweep is a one-invocation Recovery Sweep over a database.
type Sweep struct {
	db     Beginner
	opts   Options
	logger *slog.Logger
}

// NewSweep validates explicit options and creates a Sweep.
func NewSweep(db Beginner, opts Options) (*Sweep, error) {
	if db == nil {
		return nil, errors.New("recovery database is required")
	}
	if err := opts.Budget.Validate(); err != nil {
		return nil, err
	}
	if opts.StalePublishedAfter <= 0 {
		return nil, errors.New("recovery stale-published threshold must be positive")
	}
	if opts.BuildMessage == nil {
		return nil, errors.New("recovery message builder is required")
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Sweep{db: db, opts: opts, logger: logger}, nil
}

type cursor struct {
	createdAt time.Time
	id        string
}

func initialCursor() cursor {
	return cursor{
		createdAt: time.Unix(0, 0).UTC(),
		id:        "00000000-0000-0000-0000-000000000000",
	}
}

type candidateResult struct {
	cursor     cursor
	found      bool
	applied    bool
	deferred   bool
	ineligible bool
	age        time.Duration
}

type candidateFunc func(context.Context, cursor) (candidateResult, error)

// Run executes expire, repair, re-arm and stale passes in that order using
// their reserved windows. It does not loop or sleep.
func (s *Sweep) Run(ctx context.Context) (Report, error) {
	started := time.Now()
	invocationEnd := started.Add(s.opts.Budget.Total)
	report := Report{}

	plans := []struct {
		name   string
		budget PassBudget
		fn     candidateFunc
		report *PassReport
	}{
		{name: "expire", budget: s.opts.Budget.Expire, fn: s.expireCandidate, report: &report.Expire},
		{name: "repair", budget: s.opts.Budget.Repair, fn: s.repairCandidate, report: &report.Repair},
		{name: "re-arm", budget: s.opts.Budget.ReArm, fn: s.rearmCandidate, report: &report.ReArm},
		{name: "stale", budget: s.opts.Budget.Stale, fn: s.staleCandidate, report: &report.Stale},
	}

	cumulative := time.Duration(0)
	for _, plan := range plans {
		if err := ctx.Err(); err != nil {
			report.Duration = time.Since(started)
			return report, err
		}

		cumulative += plan.budget.MaxDuration
		windowEnd := started.Add(cumulative)
		if windowEnd.After(invocationEnd) {
			windowEnd = invocationEnd
		}
		passStart := time.Now()
		passEnd := passStart.Add(plan.budget.MaxDuration)
		if passEnd.After(windowEnd) {
			passEnd = windowEnd
		}
		if !passEnd.After(passStart) {
			plan.report.Pass = plan.name
			continue
		}

		passCtx, cancel := context.WithDeadline(ctx, passEnd)
		passReport, err := s.runPass(passCtx, plan.name, plan.budget.MaxCandidates, plan.fn)
		cancel()
		passReport.Duration = time.Since(passStart)
		*plan.report = passReport

		s.logger.Info("recovery pass completed",
			"pass", passReport.Pass,
			"selected", passReport.Selected,
			"applied", passReport.Applied,
			"deferred", passReport.Deferred,
			"ineligible", passReport.Ineligible,
			"oldest_candidate_age", passReport.OldestCandidateAge.String(),
			"duration", passReport.Duration.String(),
		)

		if err != nil {
			report.Duration = time.Since(started)
			return report, err
		}
	}

	report.Duration = time.Since(started)
	s.logger.Info("recovery invocation completed", "duration", report.Duration.String())
	return report, nil
}

func (s *Sweep) runPass(ctx context.Context, name string, maxCandidates int, fn candidateFunc) (PassReport, error) {
	report := PassReport{Pass: name}
	cur := initialCursor()

	for report.Selected < maxCandidates {
		if err := ctx.Err(); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return report, nil
			}
			return report, err
		}

		result, err := fn(ctx, cur)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return report, nil
			}
			if ctxErr := ctx.Err(); ctxErr != nil {
				return report, ctxErr
			}
			if !isDeferrable(err) {
				return report, err
			}
			// A deferrable error before a candidate was selected cannot make
			// progress with the same cursor, so end this pass rather than spin.
			if !result.found {
				return report, nil
			}
			// The candidate was selected and consumed attempt capacity before
			// its transaction was rolled back.
			report.Selected++
			report.Deferred++
			if result.age > report.OldestCandidateAge {
				report.OldestCandidateAge = result.age
			}
			cur = result.cursor
			continue
		}
		if !result.found {
			return report, nil
		}

		report.Selected++
		cur = result.cursor
		if result.applied {
			report.Applied++
		}
		if result.deferred {
			report.Deferred++
		}
		if result.ineligible {
			report.Ineligible++
		}
		if result.age > report.OldestCandidateAge {
			report.OldestCandidateAge = result.age
		}
	}
	return report, nil
}

type deliveryRow struct {
	id             string
	organizationID string
	eventID        string
	state          string
	dispatchSeq    int
	nextAttemptAt  time.Time
	leaseToken     string
	leaseExpiresAt pgtype.Timestamptz
	createdAt      time.Time
	eventExpiresAt time.Time
}

const deliveryColumns = `
d.id::text, d.organization_id::text, d.event_id::text, d.state, d.dispatch_seq,
d.next_attempt_at, COALESCE(d.dispatch_lease_token::text, ''), d.dispatch_lease_expires_at,
d.created_at, e.expires_at`

const expireSelectSQL = `
SELECT ` + deliveryColumns + `
  FROM deliveries d
  JOIN events e ON e.organization_id = d.organization_id AND e.id = d.event_id
 WHERE d.state IN ('pending', 'retrying')
   AND e.expires_at <= clock_timestamp()
   AND (d.created_at, d.id) > ($1::timestamptz, $2::uuid)
 ORDER BY d.created_at, d.id
 LIMIT 1
 FOR UPDATE OF d SKIP LOCKED`

const repairSelectSQL = `
SELECT ` + deliveryColumns + `
  FROM deliveries d
  JOIN events e ON e.organization_id = d.organization_id AND e.id = d.event_id
 WHERE d.state IN ('pending', 'retrying')
   AND d.next_attempt_at <= clock_timestamp()
   AND d.dispatch_lease_token IS NULL
   AND e.expires_at > clock_timestamp()
   AND NOT EXISTS (
       SELECT 1 FROM outbox_messages o
        WHERE o.delivery_id = d.id AND o.sequence = d.dispatch_seq
   )
   AND (d.created_at, d.id) > ($1::timestamptz, $2::uuid)
 ORDER BY d.created_at, d.id
 LIMIT 1
 FOR UPDATE OF d SKIP LOCKED`

const rearmSelectSQL = `
SELECT ` + deliveryColumns + `
  FROM deliveries d
  JOIN events e ON e.organization_id = d.organization_id AND e.id = d.event_id
 WHERE d.state IN ('pending', 'retrying')
   AND d.next_attempt_at <= clock_timestamp()
   AND d.dispatch_lease_token IS NULL
   AND e.expires_at > clock_timestamp()
   AND EXISTS (
       SELECT 1 FROM outbox_messages o
        WHERE o.delivery_id = d.id
          AND o.sequence = d.dispatch_seq
          AND o.status = 'failed'
   )
   AND (d.created_at, d.id) > ($1::timestamptz, $2::uuid)
 ORDER BY d.created_at, d.id
 LIMIT 1
 FOR UPDATE OF d SKIP LOCKED`

const staleSelectSQL = `
SELECT ` + deliveryColumns + `
  FROM deliveries d
  JOIN events e ON e.organization_id = d.organization_id AND e.id = d.event_id
 WHERE d.state IN ('pending', 'retrying')
   AND e.expires_at > clock_timestamp()
   AND (
       (d.dispatch_lease_token IS NOT NULL
        AND d.dispatch_lease_expires_at <= clock_timestamp())
       OR
       (d.dispatch_lease_token IS NULL
        AND d.next_attempt_at <= clock_timestamp()
        AND EXISTS (
            SELECT 1 FROM outbox_messages o
             WHERE o.delivery_id = d.id
               AND o.sequence = d.dispatch_seq
               AND o.status = 'published'
               AND o.published_at <= clock_timestamp() - ($3::bigint * interval '1 microsecond')
        ))
   )
   AND (d.created_at, d.id) > ($1::timestamptz, $2::uuid)
 ORDER BY d.created_at, d.id
 LIMIT 1
 FOR UPDATE OF d SKIP LOCKED`

const expireUpdateSQL = `
UPDATE deliveries
   SET state = 'expired',
       completed_at = $2::timestamptz,
       next_attempt_at = NULL,
       dispatch_lease_token = NULL,
       dispatch_lease_expires_at = NULL,
       updated_at = $2::timestamptz
 WHERE id = $1::uuid
   AND state IN ('pending', 'retrying')
RETURNING 1`

const insertOutboxSQL = `
INSERT INTO outbox_messages
    (id, organization_id, delivery_id, sequence, kind, topic, payload, headers,
     status, attempts, available_at, created_at, updated_at)
VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7::jsonb, $8::jsonb,
        'pending', 0, GREATEST($9::timestamptz, $10::timestamptz), $9::timestamptz, $9::timestamptz)`

const insertRepairSQL = insertOutboxSQL + `
ON CONFLICT (delivery_id, sequence) DO NOTHING
RETURNING 1`

const lockOutboxSQL = `
SELECT id::text, sequence, status, published_at
  FROM outbox_messages
 WHERE delivery_id = $1::uuid
 ORDER BY sequence, id
 FOR UPDATE NOWAIT`

const rearmUpdateSQL = `
UPDATE outbox_messages
   SET status = 'pending',
       attempts = 0,
       available_at = GREATEST(available_at, $3::timestamptz, $4::timestamptz),
       updated_at = $3::timestamptz
 WHERE id = $1::uuid
   AND delivery_id = $2::uuid
   AND sequence = $5
   AND status = 'failed'
RETURNING 1`

const staleDeliveryUpdateSQL = `
UPDATE deliveries
   SET dispatch_seq = $2,
       dispatch_lease_token = NULL,
       dispatch_lease_expires_at = NULL,
       updated_at = $3::timestamptz
 WHERE id = $1::uuid
   AND dispatch_seq = $4
   AND state IN ('pending', 'retrying')
RETURNING 1`

func (s *Sweep) expireCandidate(ctx context.Context, cur cursor) (result candidateResult, err error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return result, fmt.Errorf("begin recovery expire transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	d, found, err := s.selectDelivery(ctx, tx, expireSelectSQL, cur)
	if err != nil {
		return result, err
	}
	if !found {
		if err := tx.Commit(ctx); err != nil {
			return result, fmt.Errorf("commit empty recovery expire transaction: %w", err)
		}
		return result, nil
	}
	result.found = true
	result.cursor = cursor{createdAt: d.createdAt, id: d.id}

	t, err := sampleTime(ctx, tx)
	if err != nil {
		return result, err
	}
	result.age = t.Sub(d.createdAt)

	if d.eventExpiresAt.After(t) || (d.state != "pending" && d.state != "retrying") {
		result.ineligible = true
		if err := tx.Commit(ctx); err != nil {
			return result, fmt.Errorf("commit ineligible recovery expire transaction: %w", err)
		}
		return result, nil
	}

	var marker int
	err = tx.QueryRow(ctx, expireUpdateSQL, d.id, t).Scan(&marker)
	if errors.Is(err, pgx.ErrNoRows) {
		result.ineligible = true
		if err := tx.Commit(ctx); err != nil {
			return result, fmt.Errorf("commit stale recovery expire transaction: %w", err)
		}
		return result, nil
	}
	if err != nil {
		return result, fmt.Errorf("expire delivery: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return result, fmt.Errorf("commit recovery expire transaction: %w", err)
	}
	result.applied = true
	return result, nil
}

func (s *Sweep) repairCandidate(ctx context.Context, cur cursor) (result candidateResult, err error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return result, fmt.Errorf("begin recovery repair transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	d, found, err := s.selectDelivery(ctx, tx, repairSelectSQL, cur)
	if err != nil {
		return result, err
	}
	if !found {
		if err := tx.Commit(ctx); err != nil {
			return result, fmt.Errorf("commit empty recovery repair transaction: %w", err)
		}
		return result, nil
	}
	result.found = true
	result.cursor = cursor{createdAt: d.createdAt, id: d.id}

	t, err := sampleTime(ctx, tx)
	if err != nil {
		return result, err
	}
	result.age = t.Sub(d.createdAt)

	if !repairEligible(d, t) {
		result.ineligible = true
		if err := tx.Commit(ctx); err != nil {
			return result, fmt.Errorf("commit ineligible recovery repair transaction: %w", err)
		}
		return result, nil
	}

	exists, err := outboxExists(ctx, tx, d.id, d.dispatchSeq)
	if err != nil {
		return result, err
	}
	if exists {
		// Another creator won while this candidate was being selected. Lock and
		// revalidate the existing row; never overwrite it.
		if _, err := lockOutboxRows(ctx, tx, d.id); err != nil {
			return result, err
		}
		result.ineligible = true
		if err := tx.Commit(ctx); err != nil {
			return result, fmt.Errorf("commit duplicate recovery repair transaction: %w", err)
		}
		return result, nil
	}

	message, err := s.build(d)
	if err != nil {
		return result, err
	}
	var marker int
	err = tx.QueryRow(ctx, insertRepairSQL,
		message.ID, d.organizationID, d.id, message.Sequence, message.Kind, message.Topic,
		message.Payload, nullableJSON(message.Headers), t, d.nextAttemptAt,
	).Scan(&marker)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, lockErr := lockOutboxRows(ctx, tx, d.id); lockErr != nil {
			return result, lockErr
		}
		result.ineligible = true
		if err := tx.Commit(ctx); err != nil {
			return result, fmt.Errorf("commit repair conflict transaction: %w", err)
		}
		return result, nil
	}
	if err != nil {
		return result, fmt.Errorf("insert repaired outbox row: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return result, fmt.Errorf("commit recovery repair transaction: %w", err)
	}
	result.applied = true
	return result, nil
}

func (s *Sweep) rearmCandidate(ctx context.Context, cur cursor) (result candidateResult, err error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return result, fmt.Errorf("begin recovery re-arm transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	d, found, err := s.selectDelivery(ctx, tx, rearmSelectSQL, cur)
	if err != nil {
		return result, err
	}
	if !found {
		if err := tx.Commit(ctx); err != nil {
			return result, fmt.Errorf("commit empty recovery re-arm transaction: %w", err)
		}
		return result, nil
	}
	result.found = true
	result.cursor = cursor{createdAt: d.createdAt, id: d.id}

	rows, err := lockOutboxRows(ctx, tx, d.id)
	if err != nil {
		return result, err
	}
	t, err := sampleTime(ctx, tx)
	if err != nil {
		return result, err
	}
	result.age = t.Sub(d.createdAt)

	current, ok := rows[d.dispatchSeq]
	if !ok || current.status != "failed" {
		result.ineligible = true
		if err := tx.Commit(ctx); err != nil {
			return result, fmt.Errorf("commit ineligible recovery re-arm transaction: %w", err)
		}
		return result, nil
	}
	if !rearmEligible(d, t) {
		result.ineligible = true
		if err := tx.Commit(ctx); err != nil {
			return result, fmt.Errorf("commit stale recovery re-arm transaction: %w", err)
		}
		return result, nil
	}

	var marker int
	err = tx.QueryRow(ctx, rearmUpdateSQL, current.id, d.id, t, d.nextAttemptAt, d.dispatchSeq).Scan(&marker)
	if errors.Is(err, pgx.ErrNoRows) {
		result.ineligible = true
		if err := tx.Commit(ctx); err != nil {
			return result, fmt.Errorf("commit changed recovery re-arm transaction: %w", err)
		}
		return result, nil
	}
	if err != nil {
		return result, fmt.Errorf("re-arm failed outbox row: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return result, fmt.Errorf("commit recovery re-arm transaction: %w", err)
	}
	result.applied = true
	return result, nil
}

func (s *Sweep) staleCandidate(ctx context.Context, cur cursor) (result candidateResult, err error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return result, fmt.Errorf("begin recovery stale transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	d, found, err := s.selectDelivery(ctx, tx, staleSelectSQL, cur, s.opts.StalePublishedAfter.Microseconds())
	if err != nil {
		return result, err
	}
	if !found {
		if err := tx.Commit(ctx); err != nil {
			return result, fmt.Errorf("commit empty recovery stale transaction: %w", err)
		}
		return result, nil
	}
	result.found = true
	result.cursor = cursor{createdAt: d.createdAt, id: d.id}

	rows, err := lockOutboxRows(ctx, tx, d.id)
	if err != nil {
		return result, err
	}
	t, err := sampleTime(ctx, tx)
	if err != nil {
		return result, err
	}
	result.age = t.Sub(d.createdAt)

	if !staleEligible(d, rows, t, s.opts.StalePublishedAfter) {
		result.ineligible = true
		if err := tx.Commit(ctx); err != nil {
			return result, fmt.Errorf("commit ineligible recovery stale transaction: %w", err)
		}
		return result, nil
	}

	newSeq := d.dispatchSeq + 1
	if _, exists := rows[newSeq]; exists {
		result.ineligible = true
		if err := tx.Commit(ctx); err != nil {
			return result, fmt.Errorf("commit duplicate recovery stale transaction: %w", err)
		}
		return result, nil
	}

	message, err := s.build(deliveryWithSeq(d, newSeq))
	if err != nil {
		return result, err
	}
	var marker int
	err = tx.QueryRow(ctx, staleDeliveryUpdateSQL, d.id, newSeq, t, d.dispatchSeq).Scan(&marker)
	if errors.Is(err, pgx.ErrNoRows) {
		result.ineligible = true
		if err := tx.Commit(ctx); err != nil {
			return result, fmt.Errorf("commit changed recovery stale transaction: %w", err)
		}
		return result, nil
	}
	if err != nil {
		return result, fmt.Errorf("advance stale delivery generation: %w", err)
	}

	_, err = tx.Exec(ctx, insertOutboxSQL,
		message.ID, d.organizationID, d.id, message.Sequence, message.Kind, message.Topic,
		message.Payload, nullableJSON(message.Headers), t, d.nextAttemptAt,
	)
	if err != nil {
		return result, fmt.Errorf("insert stale redispatch outbox row: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return result, fmt.Errorf("commit recovery stale transaction: %w", err)
	}
	result.applied = true
	return result, nil
}

func (s *Sweep) selectDelivery(ctx context.Context, tx pgx.Tx, query string, cur cursor, extra ...any) (deliveryRow, bool, error) {
	args := make([]any, 0, 2+len(extra))
	args = append(args, cur.createdAt, cur.id)
	args = append(args, extra...)

	var d deliveryRow
	err := tx.QueryRow(ctx, query, args...).Scan(
		&d.id,
		&d.organizationID,
		&d.eventID,
		&d.state,
		&d.dispatchSeq,
		&d.nextAttemptAt,
		&d.leaseToken,
		&d.leaseExpiresAt,
		&d.createdAt,
		&d.eventExpiresAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return deliveryRow{}, false, nil
	}
	if err != nil {
		return deliveryRow{}, false, fmt.Errorf("select recovery candidate: %w", err)
	}
	return d, true, nil
}

func (s *Sweep) build(d deliveryRow) (Message, error) {
	message, err := s.opts.BuildMessage(Delivery{
		OrganizationID: d.organizationID,
		DeliveryID:     d.id,
		EventID:        d.eventID,
		DispatchSeq:    d.dispatchSeq,
		NextAttemptAt:  d.nextAttemptAt,
		State:          d.state,
		CreatedAt:      d.createdAt,
	})
	if err != nil {
		return Message{}, fmt.Errorf("build recovery outbox message: %w", err)
	}
	if err := validateMessage(message, d.dispatchSeq); err != nil {
		return Message{}, err
	}
	return message, nil
}

func deliveryWithSeq(d deliveryRow, sequence int) deliveryRow {
	d.dispatchSeq = sequence
	return d
}

func repairEligible(d deliveryRow, t time.Time) bool {
	if d.state != "pending" && d.state != "retrying" {
		return false
	}
	if d.leaseToken != "" {
		return false
	}
	if d.nextAttemptAt.After(t) {
		return false
	}
	return d.eventExpiresAt.After(t)
}

func rearmEligible(d deliveryRow, t time.Time) bool {
	if d.state != "pending" && d.state != "retrying" {
		return false
	}
	if d.leaseToken != "" {
		return false
	}
	if d.nextAttemptAt.After(t) {
		return false
	}
	return d.eventExpiresAt.After(t)
}

type outboxState struct {
	id          string
	sequence    int
	status      string
	publishedAt pgtype.Timestamptz
}

func staleEligible(d deliveryRow, rows map[int]outboxState, t time.Time, staleAfter time.Duration) bool {
	if d.state != "pending" && d.state != "retrying" {
		return false
	}
	if !d.eventExpiresAt.After(t) {
		return false
	}
	if d.leaseToken != "" {
		return d.leaseExpiresAt.Valid && !d.leaseExpiresAt.Time.After(t)
	}
	if d.nextAttemptAt.After(t) {
		return false
	}
	current, ok := rows[d.dispatchSeq]
	if !ok || current.status != "published" || !current.publishedAt.Valid {
		return false
	}
	return !current.publishedAt.Time.After(t.Add(-staleAfter))
}

func lockOutboxRows(ctx context.Context, tx pgx.Tx, deliveryID string) (map[int]outboxState, error) {
	rows, err := tx.Query(ctx, lockOutboxSQL, deliveryID)
	if err != nil {
		return nil, fmt.Errorf("lock delivery outbox rows: %w", err)
	}
	defer rows.Close()

	result := make(map[int]outboxState)
	for rows.Next() {
		var state outboxState
		if err := rows.Scan(&state.id, &state.sequence, &state.status, &state.publishedAt); err != nil {
			return nil, fmt.Errorf("scan locked outbox row: %w", err)
		}
		result[state.sequence] = state
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read locked outbox rows: %w", err)
	}
	return result, nil
}

func outboxExists(ctx context.Context, tx pgx.Tx, deliveryID string, sequence int) (bool, error) {
	var marker int
	err := tx.QueryRow(ctx,
		`SELECT 1 FROM outbox_messages WHERE delivery_id = $1::uuid AND sequence = $2`,
		deliveryID, sequence,
	).Scan(&marker)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check current generation outbox row: %w", err)
	}
	return true, nil
}

func sampleTime(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	var t time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&t); err != nil {
		return time.Time{}, fmt.Errorf("sample recovery database time: %w", err)
	}
	return t, nil
}

func nullableJSON(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func validateMessage(message Message, sequence int) error {
	if !validUUID(message.ID) {
		return errors.New("recovery message id is not a UUID")
	}
	if message.Sequence != sequence {
		return errors.New("recovery message sequence does not match the delivery generation")
	}
	if message.Kind != "delivery.dispatch" && message.Kind != "delivery.retry" {
		return errors.New("recovery message kind is not supported")
	}
	if strings.TrimSpace(message.Topic) == "" {
		return errors.New("recovery message topic is required")
	}
	if !json.Valid([]byte(message.Payload)) {
		return errors.New("recovery message payload is not valid JSON")
	}
	if message.Headers != "" && !json.Valid([]byte(message.Headers)) {
		return errors.New("recovery message headers are not valid JSON")
	}
	return nil
}

func isDeferrable(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "55P03", "40001", "40P01":
			return true
		}
	}
	return false
}

func validUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, r := range value {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
				return false
			}
		}
	}
	return true
}
