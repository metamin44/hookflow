package management

import (
	"context"
	"time"

	"github.com/metamin44/hookflow/internal/modules/operations"
)

// OperationalOverview returns the organization's derived operational summary
// for a window ending at the current UTC second.
func (s *Service) OperationalOverview(ctx context.Context, organizationID string, window time.Duration) (operations.Overview, error) {
	return operations.ReadOverview(ctx, s.pool, organizationID, observationWindow(window))
}

// OperationalEndpointHealth returns the organization's endpoint activity for a
// window ending at the current UTC second, ordered by failures first. The
// window is returned so the response describes the exact observed interval.
func (s *Service) OperationalEndpointHealth(ctx context.Context, organizationID string, window time.Duration, limit int) (operations.Window, []operations.EndpointHealth, bool, error) {
	observed := observationWindow(window)
	records, truncated, err := operations.ReadEndpoints(ctx, s.pool, organizationID, observed, limit)
	return observed, records, truncated, err
}

// observationWindow anchors every read to one UTC second so the current and
// previous windows cover exactly equal widths.
func observationWindow(duration time.Duration) operations.Window {
	to := time.Now().UTC().Truncate(time.Second)
	return operations.Window{From: to.Add(-duration), To: to}
}
