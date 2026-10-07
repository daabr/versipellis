package dest

import (
	"context"

	"github.com/daabr/versipellis/pkg/flow"
)

// Discard is a trivial sender that doesn't output anything, but it does release
// all the resources associated with known data types, unlike a nil sender.
var Discard = newDiscard()

type discardSender struct{}

func newDiscard() discardSender {
	return discardSender{}
}

//bodyclose:handled
func (discardSender) Send(_ context.Context, data flow.Chunk) {
	if flow.IsEmpty(data) {
		return
	}

	switch chunk := data.(type) {
	case flow.HTTPRequests:
		for _, req := range chunk {
			if req != nil && req.Body != nil {
				_ = req.Body.Close()
			}
		}
	case flow.HTTPResponses:
		for _, resp := range chunk {
			if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
		}
	}
}

func (discardSender) Close(context.Context) {}
