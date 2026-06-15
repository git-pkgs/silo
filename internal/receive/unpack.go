package receive

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/storer"
)

var (
	errPackTooLarge   = errors.New("unpack-limit: packfile exceeds MaxPackBytes")
	errTooManyObjects = errors.New("unpack-limit: packfile exceeds MaxObjects")
	errBadPackHeader  = errors.New("malformed packfile header")
)

const (
	packHeaderLen     = 12
	packSignature     = "PACK"
	packNumObjectsOff = 8
)

// unpack streams a packfile into st, enforcing the byte and object-count
// limits. The reader is fully consumed (or closed) before returning so the
// protocol stream is left at the report-status position.
func unpack(st storer.Storer, r io.ReadCloser, limits Limits) error {
	defer func() { _ = r.Close() }()

	lr := &capReader{r: r, remaining: limits.MaxPackBytes}

	hdr := make([]byte, packHeaderLen)
	if _, err := io.ReadFull(lr, hdr); err != nil {
		if errors.Is(err, errPackTooLarge) {
			return drain(r, errPackTooLarge)
		}
		return errBadPackHeader
	}
	if string(hdr[:len(packSignature)]) != packSignature {
		return errBadPackHeader
	}
	if n := binary.BigEndian.Uint32(hdr[packNumObjectsOff:]); limits.MaxObjects > 0 && n > limits.MaxObjects {
		return drain(r, fmt.Errorf("%w (%d > %d)", errTooManyObjects, n, limits.MaxObjects))
	}

	full := io.MultiReader(bytes.NewReader(hdr), lr)
	if err := packfile.UpdateObjectStorage(st, full); err != nil {
		if errors.Is(err, errPackTooLarge) {
			return drain(r, errPackTooLarge)
		}
		return err
	}
	return nil
}

func drain(r io.Reader, err error) error {
	_, _ = io.Copy(io.Discard, r)
	return err
}

// capReader is an io.Reader that fails with errPackTooLarge once more than
// remaining bytes have been requested. Unlike io.LimitedReader it returns a
// distinguishable error rather than io.EOF.
type capReader struct {
	r         io.Reader
	remaining int64
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.remaining <= 0 {
		return 0, errPackTooLarge
	}
	if int64(len(p)) > c.remaining {
		p = p[:c.remaining]
	}
	n, err := c.r.Read(p)
	c.remaining -= int64(n)
	if err == nil && c.remaining <= 0 {
		err = errPackTooLarge
	}
	return n, err
}
