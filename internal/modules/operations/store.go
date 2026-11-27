package operations

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Querier is satisfied by pgxpool.Pool and pgx.Tx. Operations never begins its
// own transaction and performs only reads.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

const deliveryCountsSQL = `
SELECT
	count(*) FILTER (WHERE d.created_at >= $2::timestamptz) AS current_total,
	count(*) FILTER (WHERE d.created_at >= $2::timestamptz AND d.generation = 1) AS current_initial,
	count(*) FILTER (WHERE d.created_at >= $2::timestamptz AND d.state = 'pending') AS current_pending,
	count(*) FILTER (WHERE d.created_at >= $2::timestamptz AND d.state = 'retrying') AS current_retrying,
	count(*) FILTER (WHERE d.created_at >= $2::timestamptz AND d.state = 'succeeded') AS current_succeeded,
	count(*) FILTER (WHERE d.created_at >= $2::timestamptz AND d.state = 'failed') AS current_failed,
	count(*) FILTER (WHERE d.created_at >= $2::timestamptz AND d.state = 'expired') AS current_expired,
	count(*) FILTER (WHERE d.created_at >= $2::timestamptz AND d.generation > 1 AND d.state = 'succeeded') AS retry_succeeded,
	count(*) FILTER (WHERE d.created_at >= $2::timestamptz AND d.generation > 1 AND d.state = 'failed') AS retry_failed,
	count(*) FILTER (WHERE d.created_at >= $2::timestamptz AND d.generation > 1 AND d.state = 'expired') AS retry_expired,
	count(*) FILTER (WHERE d.created_at >= $2::timestamptz AND d.generation > 1
		AND d.state IN ('pending', 'retrying')) AS retry_in_progress,
	count(*) FILTER (WHERE d.created_at < $2::timestamptz) AS previous_total,
	count(*) FILTER (WHERE d.created_at < $2::timestamptz AND d.state = 'pending') AS previous_pending,
	count(*) FILTER (WHERE d.created_at < $2::timestamptz AND d.state = 'retrying') AS previous_retrying,
	count(*) FILTER (WHERE d.created_at < $2::timestamptz AND d.state = 'succeeded') AS previous_succeeded,
	count(*) FILTER (WHERE d.created_at < $2::timestamptz AND d.state = 'failed') AS previous_failed,
	count(*) FILTER (WHERE d.created_at < $2::timestamptz AND d.state = 'expired') AS previous_expired
  FROM deliveries d
 WHERE d.organization_id = $1::uuid
   AND d.created_at >= $4::timestamptz
   AND d.created_at < $3::timestamptz`

const latencySQL = `
WITH recent AS (
	SELECT duration_ms,
	       row_number() OVER (ORDER BY started_at DESC, id DESC) AS rn
	  FROM delivery_attempts
	 WHERE organization_id = $1::uuid
	   AND started_at >= $2::timestamptz
	   AND started_at < $3::timestamptz
	   AND duration_ms IS NOT NULL
	 ORDER BY started_at DESC, id DESC
	 LIMIT $4
)
SELECT count(*),
       COALESCE(avg(duration_ms) FILTER (WHERE rn <= $5), 0)::float8,
       COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY duration_ms)
                FILTER (WHERE rn <= $5), 0)::float8
  FROM recent`

const endpointHealthSQL = `
SELECT e.id::text, e.name, e.status,
       a.total, a.succeeded, a.failed, a.expired, a.in_progress,
       a.last_succeeded_at, a.last_failed_at,
       a.last_succeeded_delivery_id, a.last_failed_delivery_id,
       s.recent_count, s.first_success_rn, s.recent_failures
  FROM endpoints e
  CROSS JOIN LATERAL (
	SELECT count(*) AS total,
	       count(*) FILTER (WHERE d.state = 'succeeded') AS succeeded,
	       count(*) FILTER (WHERE d.state = 'failed') AS failed,
	       count(*) FILTER (WHERE d.state = 'expired') AS expired,
	       count(*) FILTER (WHERE d.state IN ('pending', 'retrying')) AS in_progress,
	       max(d.completed_at) FILTER (WHERE d.state = 'succeeded') AS last_succeeded_at,
	       max(d.completed_at) FILTER (WHERE d.state = 'failed') AS last_failed_at,
	       (array_agg(d.id::text ORDER BY d.completed_at DESC)
	        FILTER (WHERE d.state = 'succeeded'))[1] AS last_succeeded_delivery_id,
	       (array_agg(d.id::text ORDER BY d.completed_at DESC)
	        FILTER (WHERE d.state = 'failed'))[1] AS last_failed_delivery_id
	  FROM deliveries d
	 WHERE d.organization_id = $1::uuid
	   AND d.endpoint_id = e.id
	   AND d.created_at >= $2::timestamptz
	   AND d.created_at < $3::timestamptz
  ) a
  CROSS JOIN LATERAL (
	WITH recent AS (
		SELECT d2.state,
		       row_number() OVER (ORDER BY d2.completed_at DESC, d2.id DESC) AS rn
		  FROM deliveries d2
		 WHERE d2.organization_id = $1::uuid
		   AND d2.endpoint_id = e.id
		   AND d2.state IN ('succeeded', 'failed', 'expired')
		 ORDER BY d2.completed_at DESC, d2.id DESC
		 LIMIT $5
	)
	SELECT count(*) AS recent_count,
	       min(rn) FILTER (WHERE state = 'succeeded') AS first_success_rn,
	       count(*) FILTER (WHERE state <> 'succeeded') AS recent_failures
	  FROM recent
  ) s
 WHERE e.organization_id = $1::uuid
   AND e.deleted_at IS NULL
   AND a.total > 0
 ORDER BY a.failed DESC, a.expired DESC, a.total DESC, e.name ASC, e.id
 LIMIT $4`

// ReadOverview aggregates one window and its immediately preceding window.
func ReadOverview(ctx context.Context, q Querier, organizationID string, window Window) (Overview, error) {
	previous := window.Previous()
	overview := Overview{Window: window, Previous: previous}

	if err := q.QueryRow(ctx, `
		SELECT count(*)
		  FROM events
		 WHERE organization_id = $1::uuid
		   AND received_at >= $2::timestamptz
		   AND received_at < $3::timestamptz`,
		organizationID, window.From, window.To,
	).Scan(&overview.EventsAccepted); err != nil {
		return Overview{}, fmt.Errorf("count accepted events: %w", err)
	}

	if err := readDeliveryCounts(ctx, q, organizationID, window, previous, &overview); err != nil {
		return Overview{}, err
	}

	latency, err := readLatency(ctx, q, organizationID, window)
	if err != nil {
		return Overview{}, err
	}
	overview.Latency = latency

	previousLatency, err := readLatency(ctx, q, organizationID, previous)
	if err != nil {
		return Overview{}, err
	}
	overview.PreviousLatency = previousLatency
	return overview, nil
}

func readDeliveryCounts(ctx context.Context, q Querier, organizationID string, window, previous Window, overview *Overview) error {
	var (
		currentTotal, currentInitial                                        int64
		currentPending, currentRetrying, currentSucceeded                   int64
		currentFailed, currentExpired                                       int64
		retrySucceeded, retryFailed, retryExpired, retryInProgress          int64
		previousTotal, previousPending, previousRetrying, previousSucceeded int64
		previousFailed, previousExpired                                     int64
	)
	err := q.QueryRow(ctx, deliveryCountsSQL, organizationID,
		window.From, window.To, previous.From,
	).Scan(
		&currentTotal, &currentInitial,
		&currentPending, &currentRetrying, &currentSucceeded, &currentFailed, &currentExpired,
		&retrySucceeded, &retryFailed, &retryExpired, &retryInProgress,
		&previousTotal, &previousPending, &previousRetrying, &previousSucceeded,
		&previousFailed, &previousExpired,
	)
	if err != nil {
		return fmt.Errorf("count deliveries: %w", err)
	}
	overview.Deliveries = DeliveryCounts{
		Total:     currentTotal,
		Initial:   currentInitial,
		Retry:     currentTotal - currentInitial,
		Pending:   currentPending,
		Retrying:  currentRetrying,
		Succeeded: currentSucceeded,
		Failed:    currentFailed,
		Expired:   currentExpired,
	}
	overview.PreviousDeliveries = DeliveryCounts{
		Total:     previousTotal,
		Pending:   previousPending,
		Retrying:  previousRetrying,
		Succeeded: previousSucceeded,
		Failed:    previousFailed,
		Expired:   previousExpired,
	}
	overview.Retries = RetryEffectiveness{
		Generations: overview.Deliveries.Retry,
		Succeeded:   retrySucceeded,
		Failed:      retryFailed,
		Expired:     retryExpired,
		InProgress:  retryInProgress,
	}
	return nil
}

func readLatency(ctx context.Context, q Querier, organizationID string, window Window) (Latency, error) {
	var latency Latency
	var measured int64
	if err := q.QueryRow(ctx, latencySQL, organizationID, window.From, window.To,
		latencySampleLimit+1, latencySampleLimit,
	).Scan(&measured, &latency.AvgMS, &latency.P95MS); err != nil {
		return Latency{}, fmt.Errorf("measure attempt latency: %w", err)
	}
	if measured > latencySampleLimit {
		latency.Attempts = latencySampleLimit
		latency.Sampled = true
		return latency, nil
	}
	latency.Attempts = measured
	return latency, nil
}

// ReadEndpoints returns endpoint activity ordered by failures first, then
// expirations, then volume. Only endpoints with at least one delivery created
// inside the window are included. The returned boolean reports whether more
// activity rows existed than the limit allowed.
func ReadEndpoints(ctx context.Context, q Querier, organizationID string, window Window, limit int) ([]EndpointHealth, bool, error) {
	if limit <= 0 {
		limit = DefaultEndpointLimit
	}
	if limit > MaxEndpointHealth {
		limit = MaxEndpointHealth
	}
	rows, err := q.Query(ctx, endpointHealthSQL, organizationID,
		window.From, window.To, limit+1, streakLimit)
	if err != nil {
		return nil, false, fmt.Errorf("read endpoint health: %w", err)
	}
	defer rows.Close()

	records := make([]EndpointHealth, 0, limit)
	for rows.Next() {
		var (
			health         EndpointHealth
			recentCount    int64
			recentFailures int64
			firstSuccessRN *int64
		)
		if err := rows.Scan(
			&health.ID, &health.Name, &health.Status,
			&health.Deliveries, &health.Succeeded, &health.Failed, &health.Expired, &health.InProgress,
			&health.LastSucceededAt, &health.LastFailedAt,
			&health.LastSucceededDeliveryID, &health.LastFailedDeliveryID,
			&recentCount, &firstSuccessRN, &recentFailures,
		); err != nil {
			return nil, false, fmt.Errorf("scan endpoint health: %w", err)
		}
		health.LastSucceededAt = utcOptional(health.LastSucceededAt)
		health.LastFailedAt = utcOptional(health.LastFailedAt)
		if firstSuccessRN != nil {
			health.ConsecutiveFailures = *firstSuccessRN - 1
		} else {
			health.ConsecutiveFailures = recentFailures
			health.StreakCapped = recentCount >= streakLimit
		}
		records = append(records, health)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("read endpoint health rows: %w", err)
	}
	if len(records) > limit {
		return records[:limit], true, nil
	}
	return records, false, nil
}

func utcOptional(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	normalized := value.UTC()
	return &normalized
}
