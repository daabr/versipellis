package dest

import (
	"context"
	"net/http"
)

// Discard is a simple function that doesn't output anything, but it does
// close any resources associated with the data, unlike a nil [config.Sender].
func Discard(_ context.Context, data any) {
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
