package runner

import (
	"sync"
	"unicode/utf8"
)

// BoundedBuffer is an io.Writer that retains at most limit bytes while always
// accepting the producer's full write. It keeps the prefix and records whether
// any bytes were dropped.
type BoundedBuffer struct {
	mu        sync.Mutex
	buf       []byte
	limit     int
	truncated bool
}

// NewBoundedBuffer creates a prefix-retaining buffer. A non-positive limit is
// a programmer error because it could silently discard every diagnostic.
func NewBoundedBuffer(limit int) *BoundedBuffer {
	if limit <= 0 {
		panic("runner.NewBoundedBuffer: limit must be positive")
	}
	return &BoundedBuffer{limit: limit, buf: make([]byte, 0, limit)}
}

// Write retains the bytes that fit and reports the original length so capped
// capture never causes the producer itself to fail.
func (b *BoundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	n := len(p)
	remaining := b.limit - len(b.buf)
	if remaining > 0 {
		if remaining > n {
			remaining = n
		}
		b.buf = append(b.buf, p[:remaining]...)
	}
	if remaining < n {
		b.truncated = true
	}
	return n, nil
}

// Truncated reports whether at least one byte was dropped.
func (b *BoundedBuffer) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated
}

// Len returns the number of retained bytes before UTF-8 tail cleanup.
func (b *BoundedBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.buf)
}

// String returns retained text without a partial trailing UTF-8 rune.
func (b *BoundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	end := len(b.buf)
	if b.truncated {
		for end > 0 && !utf8.Valid(b.buf[:end]) {
			end--
		}
	}
	return string(b.buf[:end])
}
