package receive

import (
	"errors"
	"io"
)

var errPackTooLarge = errors.New("unpack-limit: packfile exceeds MaxPackBytes")

// newCapReader bounds total bytes read from r. Unlike io.LimitedReader it
// returns a distinguishable error so callers can report `unpack-limit` rather
// than a generic EOF.
func newCapReader(r io.Reader, max int64) io.Reader {
	return &capReader{r: r, remaining: max}
}

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
