package api

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"
)

const captchaTTL = 5 * time.Minute

type CaptchaStore struct {
	mu      sync.Mutex
	entries map[string]captchaEntry
	now     func() time.Time
}

type captchaEntry struct {
	answer    string
	expiresAt time.Time
}

type captchaChallenge struct {
	ID       string `json:"captcha_id"`
	Question string `json:"question"`
}

func NewCaptchaStore() *CaptchaStore {
	return &CaptchaStore{
		entries: make(map[string]captchaEntry),
		now:     time.Now,
	}
}

func (s *CaptchaStore) Create() (captchaChallenge, error) {
	left, err := randomInt(1, 9)
	if err != nil {
		return captchaChallenge{}, err
	}
	right, err := randomInt(1, 9)
	if err != nil {
		return captchaChallenge{}, err
	}
	id, err := randomID()
	if err != nil {
		return captchaChallenge{}, err
	}
	challenge := captchaChallenge{
		ID:       id,
		Question: fmt.Sprintf("%d + %d = ?", left, right),
	}

	s.mu.Lock()
	s.cleanupLocked(s.now())
	s.entries[id] = captchaEntry{
		answer:    fmt.Sprintf("%d", left+right),
		expiresAt: s.now().Add(captchaTTL),
	}
	s.mu.Unlock()
	return challenge, nil
}

func (s *CaptchaStore) Verify(id string, code string) bool {
	id = strings.TrimSpace(id)
	code = strings.TrimSpace(code)
	if id == "" || code == "" {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.cleanupLocked(now)
	entry, ok := s.entries[id]
	delete(s.entries, id)
	return ok && now.Before(entry.expiresAt) && strings.EqualFold(entry.answer, code)
}

func (s *CaptchaStore) cleanupLocked(now time.Time) {
	for id, entry := range s.entries {
		if !now.Before(entry.expiresAt) {
			delete(s.entries, id)
		}
	}
}

func randomInt(min int64, max int64) (int64, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(max-min+1))
	if err != nil {
		return 0, err
	}
	return n.Int64() + min, nil
}

func randomID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}
