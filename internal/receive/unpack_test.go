package receive

import (
	"bytes"
	"errors"
	"testing"

	"github.com/go-git/go-git/v6/storage/memory"
)

func TestUnpack_BadHeader(t *testing.T) {
	st := memory.NewStorage()
	err := unpack(st, bytes.NewReader([]byte("not a packfile")), DefaultLimits())
	if !errors.Is(err, errBadPackHeader) {
		t.Errorf("err = %v, want errBadPackHeader", err)
	}
}

func TestUnpack_ShortHeader(t *testing.T) {
	st := memory.NewStorage()
	err := unpack(st, bytes.NewReader([]byte("PACK")), DefaultLimits())
	if !errors.Is(err, errBadPackHeader) {
		t.Errorf("err = %v, want errBadPackHeader", err)
	}
}

func TestCapReader(t *testing.T) {
	r := &capReader{r: bytes.NewReader(make([]byte, 100)), remaining: 10}
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
