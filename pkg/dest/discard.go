package dest

import (
	"context"
	"net/http"
)

// Discard is a trivial sender that doesn't output anything, but it does
// close any resources associated with known data types, unlike a nil sender.
var Discard = newDiscard()

type discardSender struct{}

func newDiscard() discardSender {
	return discardSender{}
}

func (discardSender) Send(_ context.Context, data any) {
	switch t := data.(type) {
	case *http.Request:
		if t != nil && t.Body != nil {
			_ = t.Body.Close()
		}
	case *http.Response:
		if t != nil && t.Body != nil {
			_ = t.Body.Close()
		}
	}
}

func (discardSender) Close(context.Context) {}
