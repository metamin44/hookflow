// Package operations derives read-only, organization-scoped operational
// aggregates from persisted delivery state. It never writes, never begins a
// transaction and never exposes payloads, receiver URLs, headers or secrets.
package operations

import (
	"math"
	"time"
)

// MaxEndpointHealth is the largest operational endpoint page the API returns.
const MaxEndpointHealth = 100

// DefaultEndpointLimit is the default operational endpoint page size.
const DefaultEndpointLimit = 50

// latencySampleLimit bounds the attempt rows used for latency percentiles in
// one window. When a window holds more attempts, the most recent
// latencySampleLimit rows are measured and Sampled is true.
const latencySampleLimit = 10000

// streakLimit bounds how many recent terminal deliveries are inspected when
// deriving an endpoint's consecutive-failure streak.
const streakLimit = 50

// Window is a half-open UTC observation window [From, To).
type Window struct {
	From time.Time
	To   time.Time
}

// Duration returns the window width.
func (w Window) Duration() time.Duration { return w.To.Sub(w.From) }

// Previous returns the equally sized window immediately before w.
func (w Window) Previous() Window {
	return Window{From: w.From.Add(-w.Duration()), To: w.From}
}

// DeliveryCounts summarizes the deliveries created inside a window. Counts are
// the delivery states observed at read time, not a replay of state transitions.
type DeliveryCounts struct {
	Total     int64
	Initial   int64
	Retry     int64
	Pending   int64
	Retrying  int64
	Succeeded int64
	Failed    int64
	Expired   int64
}

// Terminal is the number of cohort deliveries that reached a terminal state.
// Expired deliveries are terminal but were never successfully delivered.
func (c DeliveryCounts) Terminal() int64 { return c.Succeeded + c.Failed + c.Expired }

// SuccessRate returns succeeded/terminal, or nil when nothing is terminal.
func (c DeliveryCounts) SuccessRate() *float64 { return Ratio(c.Succeeded, c.Terminal()) }

// RetryEffectiveness measures Phase 10 retry generations (generation > 1)
// created inside a window.
type RetryEffectiveness struct {
	Generations int64
	Succeeded   int64
	Failed      int64
	Expired     int64
	InProgress  int64
}

// Terminal is the number of retry generations that reached a terminal state.
func (r RetryEffectiveness) Terminal() int64 { return r.Succeeded + r.Failed + r.Expired }

// SuccessRate returns succeeded/terminal, or nil when nothing is terminal.
func (r RetryEffectiveness) SuccessRate() *float64 { return Ratio(r.Succeeded, r.Terminal()) }

// Latency summarizes completed attempt durations observed inside a window.
// Attempts without a recorded duration are excluded from all three fields.
type Latency struct {
	Attempts int64
	AvgMS    float64
	P95MS    float64
	Sampled  bool
}

// Overview is the organization operational summary for one window and the
// immediately preceding window of equal size.
type Overview struct {
	Window             Window
	Previous           Window
	EventsAccepted     int64
	Deliveries         DeliveryCounts
	PreviousDeliveries DeliveryCounts
	Latency            Latency
	PreviousLatency    Latency
	Retries            RetryEffectiveness
}

// EndpointHealth is one endpoint's observed delivery activity inside a window.
// It is an observation of past outcomes, not a statement of availability.
type EndpointHealth struct {
	ID                      string
	Name                    string
	Status                  string
	Deliveries              int64
	Succeeded               int64
	Failed                  int64
	Expired                 int64
	InProgress              int64
	LastSucceededAt         *time.Time
	LastFailedAt            *time.Time
	LastSucceededDeliveryID *string
	LastFailedDeliveryID    *string
	ConsecutiveFailures     int64
	StreakCapped            bool
}

// Terminal is the number of activity deliveries that reached a terminal state.
func (e EndpointHealth) Terminal() int64 { return e.Succeeded + e.Failed + e.Expired }

// SuccessRate returns succeeded/terminal, or nil when nothing is terminal.
func (e EndpointHealth) SuccessRate() *float64 { return Ratio(e.Succeeded, e.Terminal()) }

// Ratio returns part/whole rounded to four decimal places, or nil when whole
// is not positive.
func Ratio(part, whole int64) *float64 {
	if whole <= 0 {
		return nil
	}
	value := math.Round(float64(part)/float64(whole)*10000) / 10000
	return &value
}
