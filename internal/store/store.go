package store

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

type ChallengeRecord struct {
	ExpectedHash string
	ExpiresAt    time.Time
}

type InMemoryStore struct {
	mu          sync.Mutex
	records     map[string]ChallengeRecord
	ttl         time.Duration
	stopJanitor chan struct{}
}

func NewInMemoryStore(ttl time.Duration) *InMemoryStore {
	s := &InMemoryStore{
		records:     make(map[string]ChallengeRecord),
		ttl:         ttl,
		stopJanitor: make(chan struct{}),
	}
	go s.janitor()
	return s
}

func (s *InMemoryStore) Close() {
	close(s.stopJanitor)
}

func (s *InMemoryStore) Put(challengeID string, expectedPlain []byte) {
	sha := sha256.Sum256(expectedPlain)
	rec := ChallengeRecord{
		ExpectedHash: hex.EncodeToString(sha[:]),
		ExpiresAt:    time.Now().Add(s.ttl),
	}
	s.mu.Lock()
	s.records[challengeID] = rec
	s.mu.Unlock()
}

func (s *InMemoryStore) ValidateAndDelete(challengeID string, got []byte) bool {
	sha := sha256.Sum256(got)
	gotHash := hex.EncodeToString(sha[:])
	s.mu.Lock()
	rec, ok := s.records[challengeID]
	if ok {
		delete(s.records, challengeID)
	}
	s.mu.Unlock()
	if !ok {
		return false
	}
	if time.Now().After(rec.ExpiresAt) {
		return false
	}
	return rec.ExpectedHash == gotHash
}

func (s *InMemoryStore) janitor() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopJanitor:
			return
		case <-ticker.C:
			now := time.Now()
			s.mu.Lock()
			for id, rec := range s.records {
				if now.After(rec.ExpiresAt) {
					delete(s.records, id)
				}
			}
			s.mu.Unlock()
		}
	}
}
