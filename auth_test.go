package fileprocesor

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
)

func TestNewAuthInterceptor_ValidKey(t *testing.T) {
	interceptor := NewAuthInterceptor([]string{"key-abc", "key-def"})
	next := connect.UnaryFunc(func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		caller := CallerFromContext(ctx)
		if caller.UserID != "user-123" {
			t.Errorf("UserID = %q, want %q", caller.UserID, "user-123")
		}
		if caller.ServiceID != "svc-456" {
			t.Errorf("ServiceID = %q, want %q", caller.ServiceID, "svc-456")
		}
		return nil, nil
	})

	handler := interceptor(next)

	req := connect.NewRequest[struct{}](nil)
	req.Header().Set("Authorization", "Bearer key-def")
	req.Header().Set("X-User-ID", "user-123")
	req.Header().Set("X-Service-ID", "svc-456")

	_, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewAuthInterceptor_MissingAuth(t *testing.T) {
	interceptor := NewAuthInterceptor([]string{"key-abc"})
	next := connect.UnaryFunc(func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		t.Fatal("should not be called")
		return nil, nil
	})

	handler := interceptor(next)

	req := connect.NewRequest[struct{}](nil)
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for missing auth")
	}
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeUnauthenticated {
		t.Errorf("expected Unauthenticated error, got %v", err)
	}
}

func TestNewAuthInterceptor_InvalidKey(t *testing.T) {
	interceptor := NewAuthInterceptor([]string{"key-abc"})
	next := connect.UnaryFunc(func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		t.Fatal("should not be called")
		return nil, nil
	})

	handler := interceptor(next)

	req := connect.NewRequest[struct{}](nil)
	req.Header().Set("Authorization", "Bearer wrong-key")
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for invalid key")
	}
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeUnauthenticated {
		t.Errorf("expected Unauthenticated error, got %v", err)
	}
}
