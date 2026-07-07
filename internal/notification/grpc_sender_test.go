package notification

import (
	"context"
	"net"
	"testing"
	"time"

	notifierv1 "github-release-notifier/gen/notifier/v1"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// stubServer is an in-memory NotifierService used to exercise the client end to
// end (real gRPC codec + transport) without a network or Docker.
type stubServer struct {
	notifierv1.UnimplementedNotifierServiceServer
	err error
	got *notifierv1.SendConfirmationRequest
}

func (s *stubServer) SendConfirmation(_ context.Context, req *notifierv1.SendConfirmationRequest) (*notifierv1.SendConfirmationResponse, error) {
	s.got = req
	if s.err != nil {
		return nil, s.err
	}
	return &notifierv1.SendConfirmationResponse{}, nil
}

// dialInProcess starts srv on an in-memory bufconn listener and returns a client
// connection wired to it.
func dialInProcess(t *testing.T, srv notifierv1.NotifierServiceServer) *grpc.ClientConn {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	s := grpc.NewServer()
	notifierv1.RegisterNotifierServiceServer(s, srv)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestGRPCConfirmationSender_Success(t *testing.T) {
	srv := &stubServer{}
	sender := NewGRPCConfirmationSender(dialInProcess(t, srv), 2*time.Second)

	if err := sender.SendConfirmation(context.Background(), "a@b.com", "x/y", "http://t/api/confirm/tok"); err != nil {
		t.Fatalf("SendConfirmation: %v", err)
	}
	if srv.got.GetEmail() != "a@b.com" || srv.got.GetRepo() != "x/y" || srv.got.GetConfirmUrl() != "http://t/api/confirm/tok" {
		t.Errorf("server got %+v, want the request fields propagated", srv.got)
	}
}

func TestGRPCConfirmationSender_PropagatesError(t *testing.T) {
	srv := &stubServer{err: status.Error(codes.Unavailable, "smtp down")}
	sender := NewGRPCConfirmationSender(dialInProcess(t, srv), 2*time.Second)

	err := sender.SendConfirmation(context.Background(), "a@b.com", "x/y", "http://t/api/confirm/tok")
	if err == nil {
		t.Fatal("want error when the server returns non-OK")
	}
	if status.Code(err) != codes.Unavailable {
		t.Errorf("status code = %v, want Unavailable (propagated through the wrap)", status.Code(err))
	}
}
