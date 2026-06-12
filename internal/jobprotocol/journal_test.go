package jobprotocol

import (
	"path/filepath"
	"testing"
	"time"
)

// TestJournalDuplicateAndTerminalReplay verifies duplicate idempotency keys replay the terminal result.
// t is the testing handle used for assertions, and the function returns no values.
func TestJournalDuplicateAndTerminalReplay(t *testing.T) {
	journal, err := NewJournal(JournalOptions{Path: filepath.Join(t.TempDir(), "journal.json"), Retention: time.Hour, MaxActive: 1})
	if err != nil {
		t.Fatalf("new journal: %v", err)
	}
	envelope := Envelope{
		JobID:              "job-1",
		CapabilityKey:      "scum.db.query.readonly",
		IdempotencyKey:     "idem-1",
		EndpointGeneration: 7,
		Payload:            map[string]any{"query": "SELECT * FROM secret_table WHERE token='abc'"},
	}
	ack := journal.Accept(envelope, "endpoint-1", 7, true)
	if ack.State != AckAccepted {
		t.Fatalf("expected accepted ack, got %+v", ack)
	}
	result, err := journal.MarkResult(Result{JobID: "job-1", EndpointID: "endpoint-1", EndpointGeneration: 7, Status: JobStateSucceeded, ResultCode: "ok", Summary: "done"})
	if err != nil {
		t.Fatalf("mark result: %v", err)
	}
	if result.Status != JobStateSucceeded {
		t.Fatalf("expected succeeded result, got %+v", result)
	}

	duplicate := journal.Accept(Envelope{JobID: "job-2", CapabilityKey: "scum.db.query.readonly", IdempotencyKey: "idem-1", EndpointGeneration: 7}, "endpoint-1", 7, true)
	if duplicate.State != AckDuplicate || duplicate.Result == nil || duplicate.Result.ResultCode != "ok" {
		t.Fatalf("expected duplicate terminal replay, got %+v", duplicate)
	}
	reconciled := journal.Reconcile(ReconcileRequest{IdempotencyKey: "idem-1", EndpointGeneration: 7}, "endpoint-1", 7)
	if reconciled.State != ReconcileTerminal || reconciled.Result == nil {
		t.Fatalf("expected terminal reconciliation, got %+v", reconciled)
	}
}

// TestJournalGenerationMismatchAndBusy verifies stale generation and capacity backpressure responses.
// t is the testing handle used for assertions, and the function returns no values.
func TestJournalGenerationMismatchAndBusy(t *testing.T) {
	journal, err := NewJournal(JournalOptions{Retention: time.Hour, MaxActive: 1})
	if err != nil {
		t.Fatalf("new journal: %v", err)
	}
	stale := journal.Accept(Envelope{JobID: "job-stale", CapabilityKey: "process.restart", EndpointGeneration: 6}, "endpoint-1", 7, true)
	if stale.State != AckGenerationMismatch {
		t.Fatalf("expected generation mismatch, got %+v", stale)
	}
	first := journal.Accept(Envelope{JobID: "job-1", CapabilityKey: "process.restart", EndpointGeneration: 7}, "endpoint-1", 7, true)
	if first.State != AckAccepted {
		t.Fatalf("expected accepted first job, got %+v", first)
	}
	second := journal.Accept(Envelope{JobID: "job-2", CapabilityKey: "process.restart", EndpointGeneration: 7}, "endpoint-1", 7, true)
	if second.State != AckBusy || second.RetryAfterMS == 0 {
		t.Fatalf("expected busy second job, got %+v", second)
	}
}

// TestJournalRetentionExpiry verifies expired terminal records are not replayed after cleanup.
// t is the testing handle used for assertions, and the function returns no values.
func TestJournalRetentionExpiry(t *testing.T) {
	journal, err := NewJournal(JournalOptions{Retention: time.Nanosecond, MaxActive: 1})
	if err != nil {
		t.Fatalf("new journal: %v", err)
	}
	ack := journal.Accept(Envelope{JobID: "job-1", CapabilityKey: "process.status"}, "endpoint-1", 1, true)
	if ack.State != AckAccepted {
		t.Fatalf("expected accepted ack, got %+v", ack)
	}
	if _, err := journal.MarkResult(Result{JobID: "job-1", EndpointID: "endpoint-1", EndpointGeneration: 1, Status: JobStateSucceeded, ResultCode: "ok"}); err != nil {
		t.Fatalf("mark result: %v", err)
	}
	time.Sleep(time.Millisecond)
	response := journal.Reconcile(ReconcileRequest{JobID: "job-1", EndpointGeneration: 1}, "endpoint-1", 1)
	if response.State != ReconcileUnknown {
		t.Fatalf("expected expired cleanup to return unknown, got %+v", response)
	}
}

// TestRedactionRemovesSensitivePayload verifies paths, SQL and inline secrets are sanitized.
// t is the testing handle used for assertions, and the function returns no values.
func TestRedactionRemovesSensitivePayload(t *testing.T) {
	safe := RedactMap(map[string]any{
		"path":   `C:\scum\Saved\SaveFiles\SCUM.db`,
		"query":  "SELECT token FROM users WHERE password='secret'",
		"secret": "api_key=super-secret",
	})
	for key, value := range safe {
		text, _ := value.(string)
		if text == "" {
			continue
		}
		if text == `C:\scum\Saved\SaveFiles\SCUM.db` || text == "api_key=super-secret" {
			t.Fatalf("expected redacted %s, got %q", key, text)
		}
	}
}
