package runner

import "testing"

func TestBoundedBufferRetainsPrefixAndAcceptsFullWrite(t *testing.T) {
	b := NewBoundedBuffer(5)
	n, err := b.Write([]byte("abcdefgh"))
	if err != nil || n != 8 {
		t.Fatalf("Write() = (%d, %v), want (8, nil)", n, err)
	}
	if got := b.String(); got != "abcde" {
		t.Fatalf("String() = %q, want %q", got, "abcde")
	}
	if !b.Truncated() {
		t.Fatal("Truncated() = false, want true")
	}
}

func TestBoundedBufferDropsPartialUTF8Tail(t *testing.T) {
	b := NewBoundedBuffer(4)
	_, _ = b.Write([]byte("ab€x"))
	if got := b.String(); got != "ab" {
		t.Fatalf("String() = %q, want %q", got, "ab")
	}
}

func TestBoundedBufferRejectsNonPositiveLimit(t *testing.T) {
	for _, limit := range []int{0, -1} {
		t.Run(string(rune(limit)), func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatalf("NewBoundedBuffer(%d) did not panic", limit)
				}
			}()
			_ = NewBoundedBuffer(limit)
		})
	}
}
