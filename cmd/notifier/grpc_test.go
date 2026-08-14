package main

import (
	"context"
	"errors"
	"testing"

	notifierv1 "github-release-notifier/gen/notifier/v1"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func grpcReq(email, repo, url string) *notifierv1.SendConfirmationRequest {
	return &notifierv1.SendConfirmationRequest{Email: email, Repo: repo, ConfirmUrl: url}
}

func TestGRPC_SendConfirmation_OK(t *testing.T) {
	f := &fakeSender{}
	dd := &fakeDedup{}
	s := newConfirmationServer(f, dd)

	_, err := s.SendConfirmation(context.Background(), grpcReq("a@b.com", "golang/go", "http://x/api/confirm/tok"))
	if err != nil {
		t.Fatalf("SendConfirmation err = %v, want nil (OK)", err)
	}
	if len(f.confirm) != 1 || f.confirm[0].To != "a@b.com" {
		t.Errorf("sends = %+v, want one to a@b.com", f.confirm)
	}
	// Dedup key mirrors the broker path: "confirm:{token}", token = last URL segment.
	if len(dd.marked) != 1 || dd.marked[0] != "confirm:tok" {
		t.Errorf("marked = %v, want [confirm:tok]", dd.marked)
	}
}

func TestGRPC_SendConfirmation_InvalidArgument(t *testing.T) {
	s := newConfirmationServer(&fakeSender{}, &fakeDedup{})

	cases := map[string]*notifierv1.SendConfirmationRequest{
		"empty email":   grpcReq("", "golang/go", "http://x/api/confirm/tok"),
		"empty repo":    grpcReq("a@b.com", "", "http://x/api/confirm/tok"),
		"empty confirm": grpcReq("a@b.com", "golang/go", ""),
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := s.SendConfirmation(context.Background(), req)
			if status.Code(err) != codes.InvalidArgument {
				t.Errorf("code = %v, want InvalidArgument", status.Code(err))
			}
		})
	}
}

func TestGRPC_SendConfirmation_Unavailable_OnSMTPFailure(t *testing.T) {
	f := &fakeSender{err: errors.New("smtp down")}
	s := newConfirmationServer(f, &fakeDedup{})

	_, err := s.SendConfirmation(context.Background(), grpcReq("a@b.com", "golang/go", "http://x/api/confirm/tok"))
	if status.Code(err) != codes.Unavailable {
		t.Errorf("code = %v, want Unavailable", status.Code(err))
	}
}

func TestGRPC_SendConfirmation_Duplicate_Skips(t *testing.T) {
	f := &fakeSender{}
	s := newConfirmationServer(f, &fakeDedup{seen: true})

	_, err := s.SendConfirmation(context.Background(), grpcReq("a@b.com", "golang/go", "http://x/api/confirm/tok"))
	if err != nil {
		t.Fatalf("err = %v, want nil (a duplicate is an idempotent OK)", err)
	}
	if len(f.confirm) != 0 {
		t.Errorf("sent %d, want 0 (duplicate must not re-send)", len(f.confirm))
	}
}
