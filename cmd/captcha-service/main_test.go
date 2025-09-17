package main

import (
	"context"
	"testing"
	"time"

	"github.com/matveyfed03-stack/captcha-service/internal/store"
	captcha "github.com/matveyfed03-stack/captcha-service/pb/captcha/v1"
)

func TestNewChallenge_ReturnsHTMLAndID(t *testing.T) {
	s := &captchaServer{store: store.NewInMemoryStore(time.Minute)}
	defer s.store.Close()
	resp, err := s.NewChallenge(context.Background(), &captcha.ChallengeRequest{Complexity: 0})
	if err != nil {
		t.Fatalf("NewChallenge error: %v", err)
	}
	if resp.GetChallengeId() == "" {
		t.Fatalf("empty challenge id")
	}
	if resp.GetHtml() == "" {
		t.Fatalf("empty html")
	}
}

func BenchmarkNewChallenge(b *testing.B) {
	s := &captchaServer{store: store.NewInMemoryStore(5 * time.Minute)}
	b.Cleanup(func() { s.store.Close() })
	req := &captcha.ChallengeRequest{Complexity: 0}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := s.NewChallenge(context.Background(), req)
		if err != nil {
			b.Fatalf("error: %v", err)
		}
	}
}
