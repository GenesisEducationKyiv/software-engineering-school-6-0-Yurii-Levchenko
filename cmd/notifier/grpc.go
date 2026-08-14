package main

import (
	"context"
	"log/slog"
	"path"

	notifierv1 "github-release-notifier/gen/notifier/v1"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// confirmationServer is the gRPC transport for the subscribe-saga's confirmation
// step (ADR-011). It is the synchronous sibling of the AMQP consumer: it reuses
// the SAME emailSender (SMTP) and deduper (Redis), so both transports send the
// exact same email with the same idempotency guarantee — only the transport and
// the error mapping differ.
type confirmationServer struct {
	notifierv1.UnimplementedNotifierServiceServer
	sender emailSender
	dedup  deduper
}

func newConfirmationServer(sender emailSender, dedup deduper) *confirmationServer {
	return &confirmationServer{sender: sender, dedup: dedup}
}

// SendConfirmation sends one confirmation email synchronously and reports the
// outcome via the gRPC status code (this is idiomatic gRPC — the status, not a
// field in the response body, carries success/failure):
//   - codes.OK          — email sent (or a duplicate we safely skip);
//   - codes.InvalidArgument — missing required fields (a client bug);
//   - codes.Unavailable — the SMTP backend failed.
//
// The orchestrator (client) completes the saga on OK and compensates on any
// non-OK — the same effect as the async "failed" reply on the broker path.
func (s *confirmationServer) SendConfirmation(ctx context.Context, req *notifierv1.SendConfirmationRequest) (*notifierv1.SendConfirmationResponse, error) {
	if req.GetEmail() == "" || req.GetRepo() == "" || req.GetConfirmUrl() == "" {
		return nil, status.Error(codes.InvalidArgument, "email, repo and confirm_url are required")
	}

	// Same idempotency key as the broker path (its MessageId is "confirm:{token}"),
	// so a confirmation is never sent twice — even if someone switches transport
	// mid-flight. The token is the last path segment of the confirm URL, which the
	// orchestrator builds as "<baseURL>/api/confirm/<token>".
	messageID := "confirm:" + path.Base(req.GetConfirmUrl())
	if seen, err := s.dedup.AlreadyProcessed(ctx, messageID); err != nil {
		// Fail open: a Redis blip shouldn't block delivery; a rare duplicate email
		// beats a lost confirmation.
		slog.Warn("gRPC dedup check failed, sending anyway", "message_id", messageID, "err", err)
	} else if seen {
		slog.Info("gRPC skipping duplicate confirmation", "message_id", messageID)
		return &notifierv1.SendConfirmationResponse{}, nil
	}

	if err := s.sender.SendConfirmationEmail(req.GetEmail(), req.GetConfirmUrl()); err != nil {
		slog.Error("gRPC confirmation email failed", "err", err)
		return nil, status.Errorf(codes.Unavailable, "sending confirmation email: %v", err)
	}

	if err := s.dedup.MarkProcessed(ctx, messageID); err != nil {
		slog.Warn("gRPC failed to mark processed", "message_id", messageID, "err", err)
	}
	slog.Info("Confirmation email sent", "transport", "grpc")
	return &notifierv1.SendConfirmationResponse{}, nil
}
