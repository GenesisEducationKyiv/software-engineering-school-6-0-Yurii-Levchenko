package notification

import (
	"context"
	"fmt"
	"time"

	notifierv1 "github-release-notifier/gen/notifier/v1"

	"google.golang.org/grpc"
)

// GRPCConfirmationSender is the monolith's gRPC client for the notifier's
// confirmation RPC (ADR-011). It satisfies the orchestrator's confirmationSender
// interface, so wiring it in (vs leaving it nil) switches the confirmation
// transport from the async broker to synchronous gRPC without changing the
// orchestrator (DIP: the orchestrator depends on the interface, not this type).
type GRPCConfirmationSender struct {
	client  notifierv1.NotifierServiceClient
	timeout time.Duration
}

// NewGRPCConfirmationSender wraps an already-dialed gRPC connection. The caller
// owns the connection's lifecycle (Close on shutdown). We take the connection as
// an interface so tests can pass an in-memory (bufconn) connection.
func NewGRPCConfirmationSender(conn grpc.ClientConnInterface, timeout time.Duration) *GRPCConfirmationSender {
	return &GRPCConfirmationSender{
		client:  notifierv1.NewNotifierServiceClient(conn),
		timeout: timeout,
	}
}

// SendConfirmation calls the notifier synchronously. A non-OK gRPC status comes
// back as a non-nil error (carrying the status code), which the orchestrator maps
// to saga compensation; nil means the email was sent.
func (s *GRPCConfirmationSender) SendConfirmation(ctx context.Context, email, repo, confirmURL string) error {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	_, err := s.client.SendConfirmation(ctx, &notifierv1.SendConfirmationRequest{
		Email:      email,
		Repo:       repo,
		ConfirmUrl: confirmURL,
	})
	if err != nil {
		return fmt.Errorf("grpc send confirmation: %w", err)
	}
	return nil
}
