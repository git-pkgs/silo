package receive

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp/capability"
)

// FuzzCommandList feeds arbitrary bytes to ReceivePack. The decoder, unpacker,
// and hook path must never panic on hostile input; returning an error or a
// failed report-status is fine.
func FuzzCommandList(f *testing.F) {
	src, head := newSourceCommit(f)
	seeds := [][]byte{
		nil,
		[]byte("0000"),
		[]byte("garbage that is not pkt-line"),
		encodePush(f, src.Storer, []*packp.Command{
			{Name: testDefaultBranch, Old: plumbing.ZeroHash, New: head},
		}, capability.ReportStatus).Bytes(),
		encodePush(f, src.Storer, []*packp.Command{
			{Name: testDefaultBranch, Old: head, New: plumbing.ZeroHash},
		}, capability.ReportStatus, capability.Sideband64k).Bytes(),
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, in []byte) {
		target := newBareRepo(t)
		const maxBytes = 1 << 16
		_ = ReceivePack(context.Background(), target,
			bytes.NewReader(in), io.Discard, nil,
			Limits{MaxPackBytes: maxBytes, MaxObjects: 100})
	})
}
