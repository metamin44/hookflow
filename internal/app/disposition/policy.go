// Package disposition implements the approved broker-independent RB-4 policy.
//
// It contains no RabbitMQ client and no broker adapter. The returned actions
// describe what an eventual adapter must do:
//
//   - ActionAck — basic.ack, permitted only after the required durable
//     database disposition or durable state check has been established.
//   - ActionRejectRequeue — basic.reject(requeue=true) for transient
//     infrastructure failures.
//   - ActionHold — no broker frame; leave the message unacknowledged so the
//     broker can redeliver it after channel close or consumer timeout.
//
// There is deliberately no NACK action: the approved policy does not use a
// normal basic.nack on any path.
package disposition

import "sync"

// Outcome is the caller-established result of processing one broker message.
// The caller must set Durable only after the database state required by RB-4
// has actually been committed or verified.
type Outcome int

const (
	// OutcomeSucceeded is a durable completion that transitioned the delivery
	// to succeeded.
	OutcomeSucceeded Outcome = iota + 1
	// OutcomeRetryScheduled is a durable completion that scheduled a retry.
	OutcomeRetryScheduled
	// OutcomeFailed is a durable completion that made the delivery terminal.
	OutcomeFailed
	// OutcomeExpired is a durable completion or disposition that expired the
	// delivery at the event horizon.
	OutcomeExpired
	// OutcomeStale is a message whose dispatch generation is no longer current;
	// the current generation was verified against durable state.
	OutcomeStale
	// OutcomeObsolete is a message for a delivery that is durably terminal or
	// otherwise no longer actionable.
	OutcomeObsolete
	// OutcomeDuplicate is re-delivered work whose disposition was already
	// durably recorded.
	OutcomeDuplicate
	// OutcomeInvalid is a message that cannot be mapped to approved work; the
	// caller must have established the durable state check first.
	OutcomeInvalid
	// OutcomeTransientFailure is a database or infrastructure failure that may
	// succeed on redelivery.
	OutcomeTransientFailure
	// OutcomeUncertain is a panic, cancellation or unknown commit result. It is
	// never acknowledged.
	OutcomeUncertain
)

// Action is the broker-independent disposition action.
type Action string

// Broker actions implement RB-4 without a normal NACK operation.
const (
	ActionAck           Action = "ack"
	ActionRejectRequeue Action = "reject_requeue"
	ActionHold          Action = "hold"
)

// Decision is the policy result for one message.
type Decision struct {
	Action Action
	Reason string
}

// Decide maps one processing outcome to the approved broker action. It fails
// closed: any missing durable evidence, unknown outcome or uncertain result is
// held rather than acknowledged.
func Decide(outcome Outcome, durable bool) Decision {
	switch outcome {
	case OutcomeSucceeded, OutcomeRetryScheduled, OutcomeFailed, OutcomeExpired:
		if !durable {
			return Decision{Action: ActionHold, Reason: "durable disposition not established"}
		}
		return Decision{Action: ActionAck, Reason: "durable disposition committed"}
	case OutcomeStale, OutcomeObsolete, OutcomeDuplicate, OutcomeInvalid:
		if !durable {
			return Decision{Action: ActionHold, Reason: "required durable state check not established"}
		}
		return Decision{Action: ActionAck, Reason: "durable state check established"}
	case OutcomeTransientFailure:
		return Decision{Action: ActionRejectRequeue, Reason: "transient infrastructure failure"}
	case OutcomeUncertain:
		return Decision{Action: ActionHold, Reason: "outcome uncertain"}
	default:
		return Decision{Action: ActionHold, Reason: "unknown outcome"}
	}
}

// Phase is the graceful-drain phase.
type Phase string

// Drain phases make shutdown progress observable.
const (
	PhaseRunning  Phase = "running"
	PhaseDraining Phase = "draining"
	PhaseClosed   Phase = "closed"
)

// Drain tracks in-flight work during graceful shutdown. Messages that were
// delivered by the broker but never started stay unacknowledged; no NACK is
// issued. The channel closes only after in-flight work has finished or failed,
// and the connection closes after the channel.
type Drain struct {
	mu       sync.Mutex
	inflight int
	phase    Phase
}

// NewDrain returns a drain tracker in the running phase.
func NewDrain() *Drain {
	return &Drain{phase: PhaseRunning}
}

// Begin stops accepting new work. In-flight work continues so it can commit and
// ACK. Begin is idempotent.
func (d *Drain) Begin() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.phase == PhaseRunning {
		d.phase = PhaseDraining
		d.closeIfIdle()
	}
}

// Start records that one message's processing has begun. It returns false
// during draining or after closure, in which case the message must not be
// processed and must not be acknowledged or negatively acknowledged.
func (d *Drain) Start() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.phase != PhaseRunning {
		return false
	}
	d.inflight++
	return true
}

// Finish records that one started message has reached a final disposition. It
// returns true when the channel may be closed.
func (d *Drain) Finish() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.inflight > 0 {
		d.inflight--
	}
	d.closeIfIdle()
	return d.phase == PhaseClosed
}

// Phase reports the current drain phase.
func (d *Drain) Phase() Phase {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.phase
}

// InFlight reports the number of started, unfinished messages.
func (d *Drain) InFlight() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.inflight
}

// CanCloseChannel reports whether all in-flight work is finished and the
// consumer channel may be closed. The connection must close after the channel.
func (d *Drain) CanCloseChannel() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.phase == PhaseClosed
}

func (d *Drain) closeIfIdle() {
	if d.phase == PhaseDraining && d.inflight == 0 {
		d.phase = PhaseClosed
	}
}
