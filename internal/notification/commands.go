package notification

// ConfirmationRequest and ReleaseRequest are the JSON bodies of the
// notification commands published to and consumed from the broker. They are the
// wire contract shared by the publisher (monolith) and the consumer (notifier).
type ConfirmationRequest struct {
	// SagaID correlates the command with its saga so the notifier can reply with
	// the outcome; empty for any non-saga publish.
	SagaID     string `json:"saga_id,omitempty"`
	To         string `json:"to"`
	ConfirmURL string `json:"confirm_url"`
}

type ReleaseRequest struct {
	To             string `json:"to"`
	Repo           string `json:"repo"`
	Tag            string `json:"tag"`
	UnsubscribeURL string `json:"unsubscribe_url"`
}

// SagaReply is the notifier's reply to the orchestrator reporting the outcome of
// a saga step (currently the confirmation-email send). The orchestrator consumes
// it to advance the saga's state.
type SagaReply struct {
	SagaID string `json:"saga_id"`
	Status string `json:"status"`
}

const (
	// SagaStatusSent means the saga's email step completed successfully.
	SagaStatusSent = "sent"
	// SagaStatusFailed means the email could not be sent after retries; the
	// orchestrator compensates (marks the subscription failed).
	SagaStatusFailed = "failed"
)
