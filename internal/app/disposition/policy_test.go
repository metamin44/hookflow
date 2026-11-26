package disposition

import "testing"

func TestDecideDurableDispositionsAck(t *testing.T) {
	for _, outcome := range []Outcome{
		OutcomeSucceeded,
		OutcomeRetryScheduled,
		OutcomeFailed,
		OutcomeExpired,
	} {
		decision := Decide(outcome, true)
		if decision.Action != ActionAck {
			t.Fatalf("outcome %d: action = %q, want %q", outcome, decision.Action, ActionAck)
		}
	}
}

func TestDecideCommittedWithoutDurabilityHolds(t *testing.T) {
	for _, outcome := range []Outcome{
		OutcomeSucceeded,
		OutcomeRetryScheduled,
		OutcomeFailed,
		OutcomeExpired,
	} {
		decision := Decide(outcome, false)
		if decision.Action != ActionHold {
			t.Fatalf("outcome %d without durability: action = %q, want %q", outcome, decision.Action, ActionHold)
		}
	}
}

func TestDecideDurableNoOpDispositionsAck(t *testing.T) {
	for _, outcome := range []Outcome{
		OutcomeStale,
		OutcomeObsolete,
		OutcomeDuplicate,
		OutcomeInvalid,
	} {
		decision := Decide(outcome, true)
		if decision.Action != ActionAck {
			t.Fatalf("outcome %d: action = %q, want %q", outcome, decision.Action, ActionAck)
		}
		decision = Decide(outcome, false)
		if decision.Action != ActionHold {
			t.Fatalf("outcome %d without durable check: action = %q, want %q", outcome, decision.Action, ActionHold)
		}
	}
}

func TestDecideTransientFailureRequeues(t *testing.T) {
	for _, durable := range []bool{false, true} {
		decision := Decide(OutcomeTransientFailure, durable)
		if decision.Action != ActionRejectRequeue {
			t.Fatalf("transient failure (durable=%v): action = %q, want %q", durable, decision.Action, ActionRejectRequeue)
		}
	}
}

func TestDecideUncertainNeverAcks(t *testing.T) {
	for _, durable := range []bool{false, true} {
		decision := Decide(OutcomeUncertain, durable)
		if decision.Action != ActionHold {
			t.Fatalf("uncertain outcome (durable=%v): action = %q, want %q", durable, decision.Action, ActionHold)
		}
	}
}

func TestDecideUnknownOutcomeFailsClosed(t *testing.T) {
	for _, outcome := range []Outcome{0, -1, 99} {
		decision := Decide(outcome, true)
		if decision.Action != ActionHold {
			t.Fatalf("unknown outcome %d: action = %q, want %q", outcome, decision.Action, ActionHold)
		}
	}
}

func TestDecideNeverProducesNack(t *testing.T) {
	outcomes := []Outcome{
		OutcomeSucceeded, OutcomeRetryScheduled, OutcomeFailed, OutcomeExpired,
		OutcomeStale, OutcomeObsolete, OutcomeDuplicate, OutcomeInvalid,
		OutcomeTransientFailure, OutcomeUncertain, 0, 42,
	}
	for _, outcome := range outcomes {
		for _, durable := range []bool{false, true} {
			action := Decide(outcome, durable).Action
			switch action {
			case ActionAck, ActionRejectRequeue, ActionHold:
			default:
				t.Fatalf("outcome %d produced unsupported action %q", outcome, action)
			}
		}
	}
}

func TestDrainCompletesInFlightWork(t *testing.T) {
	drain := NewDrain()
	if drain.Phase() != PhaseRunning {
		t.Fatalf("initial phase = %q", drain.Phase())
	}
	firstStart, secondStart := drain.Start(), drain.Start()
	if !firstStart || !secondStart {
		t.Fatal("running drain did not accept both new work items")
	}
	if drain.InFlight() != 2 {
		t.Fatalf("in-flight = %d, want 2", drain.InFlight())
	}

	drain.Begin()
	if drain.Phase() != PhaseDraining {
		t.Fatalf("phase after Begin = %q", drain.Phase())
	}
	if drain.Start() {
		t.Fatal("draining drain accepted new work")
	}
	if drain.CanCloseChannel() {
		t.Fatal("channel closeable with in-flight work")
	}

	if drain.Finish() {
		t.Fatal("Finish() requested channel close with one message in flight")
	}
	if !drain.Finish() {
		t.Fatal("Finish() did not request channel close when idle")
	}
	if drain.Phase() != PhaseClosed || !drain.CanCloseChannel() {
		t.Fatalf("drain did not close: phase=%q", drain.Phase())
	}
	if drain.Start() {
		t.Fatal("closed drain accepted new work")
	}
}

func TestDrainClosesImmediatelyWhenIdle(t *testing.T) {
	drain := NewDrain()
	drain.Begin()
	if drain.Phase() != PhaseClosed || !drain.CanCloseChannel() {
		t.Fatalf("idle drain did not close: phase=%q", drain.Phase())
	}
	if !drain.Finish() {
		t.Fatal("Finish() after closure no longer reported a closed channel")
	}
	if drain.Phase() != PhaseClosed {
		t.Fatalf("phase after extra Finish = %q", drain.Phase())
	}
	if drain.InFlight() != 0 {
		t.Fatalf("in-flight = %d, want 0", drain.InFlight())
	}
}

func TestDrainFinishWithoutInflightDoesNotUnderflow(t *testing.T) {
	drain := NewDrain()
	if drain.Finish() {
		t.Fatal("Finish() on running empty drain requested closure")
	}
	if drain.InFlight() != 0 {
		t.Fatalf("in-flight = %d, want 0", drain.InFlight())
	}
	drain.Begin()
	if !drain.Finish() {
		t.Fatal("Finish() after Begin did not report closure")
	}
}
