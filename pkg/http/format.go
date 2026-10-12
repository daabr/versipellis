package http

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/daabr/versipellis/pkg/codec"
	"github.com/daabr/versipellis/pkg/config"
	"github.com/daabr/versipellis/pkg/dest"
	"github.com/daabr/versipellis/pkg/flow"
)

// responseBody returns the body of a collected HTTP response without copying it, and without consuming it, so the
// response can still be passed as-is to a fallback sender (e.g., the DLQ). Collected responses always have a
// reusable body, so reading other kinds is only a fallback, which replaces the body with a reusable one.
func responseBody(resp *http.Response) ([]byte, error) {
	if resp.Body == nil {
		return nil, nil
	}
	if body, ok := resp.Body.(*reusableBody); ok {
		return body.raw, nil
	}

	// Subtle point: we shouldn't normally reach this point because collected responses should always have
	// a reusable body. But just in case, it's worth noting that an error in [io.ReadAll] causes us to lose
	// the original body when we send the response to a fallback sender. There's no way to avoid/recover
	// this loss (which is another reason why we depend on [reusableBody], not just for I/O speed).
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read HTTP response body: %w", err)
	}

	resp.Body = &reusableBody{Reader: bytes.NewReader(body), raw: body}
	return body, nil
}

// bodyChunk converts the body of an HTTP request or response, after discarding the rest of it,
// into a [flow.Chunk] of data in the given non-default format: [flow.Blobs] for [codec.FormatRaw],
// or [flow.JSON] objects parsed from the body (which they reference, see [codec.DecodeJSON]). It may
// also return a nil chunk - with a body parsing error, or a nil error if there's nothing to send.
func bodyChunk(body []byte, format config.Format) (flow.Chunk, error) {
	switch format {
	case codec.FormatRaw:
		if len(body) == 0 {
			return nil, nil
		}
		return flow.Blobs{body}, nil

	case codec.FormatJSON, codec.FormatJSONArray, codec.FormatNDJSON:
		chunk, err := codec.DecodeJSON(body)
		if err != nil {
			return nil, fmt.Errorf("invalid %s payload: %w", format, err)
		}
		if len(chunk) == 0 {
			return nil, nil
		}
		return chunk, nil

	default: // Including [codec.FormatDefault]: callers should pass the request/response message as-is in that case.
		return nil, fmt.Errorf("unexpected format %q for HTTP body", format)
	}
}

// deadLetter tries to send data that can't be processed or sent as configured to the DLQ,
// or discards it. It returns the name of the sender type that received the data, for logging.
func deadLetter(ctx context.Context, send config.SendFunc, data flow.Chunk, format config.Format) string {
	if send != nil {
		send(ctx, data, format)
		return config.SenderTypeDLQ
	}
	dest.Discard.Send(ctx, data, format)
	return config.SenderTypeDiscard
}
