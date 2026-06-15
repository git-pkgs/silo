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

func BenchmarkReceive_SingleRef(b *testing.B) {
	src, head := newSourceCommit(b)
	wire := encodePush(b, src.Storer, []*packp.Command{
		{Name: refMain, Old: plumbing.ZeroHash, New: head},
	}, capability.ReportStatus).Bytes()

	b.ReportAllocs()
	for b.Loop() {
		target := newBareRepo(b)
		if err := ReceivePack(context.Background(), target,
			bytes.NewReader(wire), io.Discard, nil, DefaultLimits()); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPktlineRead(b *testing.B) {
	src, head := newSourceCommit(b)
	wire := encodePush(b, src.Storer, []*packp.Command{
		{Name: refMain, Old: plumbing.ZeroHash, New: head},
	}, capability.ReportStatus).Bytes()

	b.ReportAllocs()
	for b.Loop() {
		req := packp.NewReferenceUpdateRequest()
		if err := req.Decode(bytes.NewReader(wire)); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAdvertise(b *testing.B) {
	src, _ := newSourceCommit(b)
	b.ReportAllocs()
	for b.Loop() {
		if err := Advertise(src, io.Discard); err != nil {
			b.Fatal(err)
		}
	}
}
