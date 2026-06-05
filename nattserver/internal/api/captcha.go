package api

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math/big"
	"strings"
	"sync"
	"time"
)

const captchaTTL = 5 * time.Minute

var captchaTestAnswers sync.Map

type CaptchaStore struct {
	mu      sync.Mutex
	entries map[string]captchaEntry
	now     func() time.Time
}

type captchaEntry struct {
	question  string
	answer    string
	expiresAt time.Time
}

type captchaChallenge struct {
	ID       string `json:"captcha_id"`
	ImageURL string `json:"image_url"`
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
		ID: id,
	}
	question := fmt.Sprintf("%d + %d = ?", left, right)

	s.mu.Lock()
	s.cleanupLocked(s.now())
	s.entries[id] = captchaEntry{
		question:  question,
		answer:    fmt.Sprintf("%d", left+right),
		expiresAt: s.now().Add(captchaTTL),
	}
	s.mu.Unlock()
	captchaTestAnswers.Store(id, fmt.Sprintf("%d", left+right))
	return challenge, nil
}

func (s *CaptchaStore) Image(id string) ([]byte, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("captcha id is required")
	}
	s.mu.Lock()
	now := s.now()
	s.cleanupLocked(now)
	entry, ok := s.entries[id]
	s.mu.Unlock()
	if !ok || !now.Before(entry.expiresAt) {
		return nil, fmt.Errorf("captcha not found or expired")
	}
	return renderCaptchaPNG(entry.question)
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
	captchaTestAnswers.Delete(id)
	return ok && now.Before(entry.expiresAt) && strings.EqualFold(entry.answer, code)
}

func captchaAnswerForTest(id string) string {
	value, _ := captchaTestAnswers.Load(id)
	answer, _ := value.(string)
	return answer
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

var glyphs = map[rune][]string{
	'0': {"111", "101", "101", "101", "101", "101", "111"},
	'1': {"010", "110", "010", "010", "010", "010", "111"},
	'2': {"111", "001", "001", "111", "100", "100", "111"},
	'3': {"111", "001", "001", "111", "001", "001", "111"},
	'4': {"101", "101", "101", "111", "001", "001", "001"},
	'5': {"111", "100", "100", "111", "001", "001", "111"},
	'6': {"111", "100", "100", "111", "101", "101", "111"},
	'7': {"111", "001", "001", "010", "010", "010", "010"},
	'8': {"111", "101", "101", "111", "101", "101", "111"},
	'9': {"111", "101", "101", "111", "001", "001", "111"},
	'+': {"000", "010", "010", "111", "010", "010", "000"},
	'=': {"000", "000", "111", "000", "111", "000", "000"},
	'?': {"111", "001", "001", "010", "010", "000", "010"},
}

func renderCaptchaPNG(text string) ([]byte, error) {
	const scale = 4
	img := image.NewRGBA(image.Rect(0, 0, 150, 44))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: color.RGBA{R: 248, G: 250, B: 252, A: 255}}, image.Point{}, draw.Src)

	ink := color.RGBA{R: 30, G: 41, B: 59, A: 255}
	x := 12
	for _, ch := range text {
		if ch == ' ' {
			x += 8
			continue
		}
		drawGlyph(img, ch, x, 8, scale, ink)
		x += 18
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func drawGlyph(img *image.RGBA, ch rune, x int, y int, scale int, ink color.Color) {
	rows, ok := glyphs[ch]
	if !ok {
		return
	}
	for row, pattern := range rows {
		for col, bit := range pattern {
			if bit != '1' {
				continue
			}
			rect := image.Rect(x+col*scale, y+row*scale, x+(col+1)*scale, y+(row+1)*scale)
			draw.Draw(img, rect, &image.Uniform{C: ink}, image.Point{}, draw.Src)
		}
	}
}
