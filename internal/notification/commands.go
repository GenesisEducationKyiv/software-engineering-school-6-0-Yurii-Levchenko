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
