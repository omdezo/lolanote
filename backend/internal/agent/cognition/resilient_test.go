package cognition

import (
	"context"
	"errors"
	"testing"
	"time"
)

type stub struct {
	name  string
	fails int // fail this many times before succeeding
	err   error
	calls int
}

func (s *stub) Name() string  { return s.name }
func (s *stub) Model() string { return s.name + "-model" }
func (s *stub) Complete(context.Context, Request) (*Response, error) {
	s.calls++
	if s.calls <= s.fails {
		return nil, s.err
	}
	return &Response{Text: s.name}, nil
}

func newTestFallback(ps ...Provider) *Fallback {
	f := NewFallback(ps...)
	f.sleep = func(time.Duration) {} // no real waiting in tests
	return f
}

func TestFallback_RetriesTransientThenSucceeds(t *testing.T) {
	p := &stub{name: "primary", fails: 2, err: ErrUnavailable}
	got, err := newTestFallback(p).Complete(context.Background(), Request{})
	if err != nil {
		t.Fatalf("want success after retries, got %v", err)
	}
	if got.Text != "primary" || p.calls != 3 {
		t.Fatalf("calls=%d text=%q; want 3 calls on the primary", p.calls, got.Text)
	}
}

func TestFallback_MovesToTheNextProviderAndReportsIt(t *testing.T) {
	a := &stub{name: "a", fails: 99, err: ErrUnavailable}
	b := &stub{name: "b"}
	f := newTestFallback(a, b)
	got, err := f.Complete(context.Background(), Request{})
	if err != nil {
		t.Fatalf("fallback did not reach the second provider: %v", err)
	}
	if got.Text != "b" {
		t.Fatalf("answered by %q, want b", got.Text)
	}
	// Silent substitution is the thing to avoid: a run that quietly used a
	// different model must still say so.
	if f.Model() != "b-model" {
		t.Fatalf("Model() reports %q; it must name the provider that answered", f.Model())
	}
}

func TestFallback_DoesNotRetryARefusal(t *testing.T) {
	// A refusal is a content decision. Asking again wastes tokens and returns
	// the same answer.
	p := &stub{name: "primary", fails: 99, err: ErrRefused}
	other := &stub{name: "other"}
	f := newTestFallback(p, other)
	if _, err := f.Complete(context.Background(), Request{}); !errors.Is(err, ErrRefused) {
		t.Fatalf("want the refusal surfaced, got %v", err)
	}
	if p.calls != 1 {
		t.Fatalf("a refusal was retried %d times; it must not be", p.calls)
	}
	if other.calls != 0 {
		t.Fatal("a refusal must not fall through to another provider")
	}
}
