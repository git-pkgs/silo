package receive

import (
	"bytes"
	"errors"
	"testing"
)

func TestCapReader(t *testing.T) {
	r := newCapReader(bytes.NewReader(make([]byte, 100)), 10)
	buf := make([]byte, 100)
	n, err := r.Read(buf)
	if n != 10 {
		t.Errorf("n = %d, want 10", n)
	}
	if !errors.Is(err, errPackTooLarge) {
		t.Errorf("err = %v, want errPackTooLarge", err)
	}
	if _, err := r.Read(buf); !errors.Is(err, errPackTooLarge) {
		t.Errorf("second read err = %v", err)
	}
}
