package flow

import (
	"net/http"
)

// Kind is the closed set of data types that may flow from collectors and receivers to senders.
type Kind interface {
	[]byte | map[string]any | *http.Request | *http.Response
}

// ChunkOf is a flat and homogeneous sequence of non-nil items. Senders take ownership of chunks and their items,
// so producers must not modify or reuse them after sending. Chunk boundaries carry no meaning: senders may split
// or merge chunks freely, and batch sizes are always measured in number of items, not number of chunks.
type ChunkOf[T Kind] []T

// Aliases for all the [ChunkOf] instantiations, for readability (e.g., in type switches).
type (
	// Blobs contains opaque but non-mergeable byte payloads.
	Blobs = ChunkOf[[]byte]
	// HTTPRequests contains requests from HTTP receivers, which are proxied one by one.
	HTTPRequests = ChunkOf[*http.Request]
	// HTTPResponses contains responses from HTTP collectors, which are relayed one by one.
	HTTPResponses = ChunkOf[*http.Response]
	// Structured contains structured records, such as decoded JSON maps and SQL rows.
	Structured = ChunkOf[map[string]any]
)

// Compile-time checks that all the [ChunkOf] aliases implement the [Chunk] interface.
var (
	_ Chunk = Blobs(nil)
	_ Chunk = HTTPRequests(nil)
	_ Chunk = HTTPResponses(nil)
	_ Chunk = Structured(nil)
)

// Chunk is the non-generic interface of all [ChunkOf] types, so that non-generic interfaces and functions (e.g.,
// a sender's Send method) can accept chunks of any kind. It is sealed: only types in this package implement it.
//
// Attention: a nil Chunk interface value has no dynamic type, so calling its Len method panics.
// Use [IsEmpty] to check for both nil and empty chunks.
type Chunk interface {
	Len() int
	sealed()
}

// Len returns the number of items/records/elements in the chunk.
func (c ChunkOf[T]) Len() int {
	return len(c)
}

func (ChunkOf[T]) sealed() {}

// IsEmpty reports whether the given chunk is nil (with or without a dynamic type) or
// has no items. Senders should call it before anything else, and ignore empty chunks.
func IsEmpty(c Chunk) bool {
	return c == nil || c.Len() == 0
}

// FakeDataType is a placeholder type that implements the [Chunk] interface but carries no data.
type FakeDataType struct{}

// Len returns the number of items/records/elements in the chunk.
func (FakeDataType) Len() int { return 1 }

func (FakeDataType) sealed() {}
