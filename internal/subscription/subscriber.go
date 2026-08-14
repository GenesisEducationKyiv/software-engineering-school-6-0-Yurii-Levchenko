package subscription

// Subscriber is the slim, public view of a subscription that other domains
// (the release scanner) need to send a notification. It deliberately omits
// the rest of the entity so callers don't depend on the full Subscription —
// this is the anti-corruption boundary. ID is kept so callers log the
// subscription_id (never the email) for PII reasons.
type Subscriber struct {
	ID    int
	Email string
	Token string
}
