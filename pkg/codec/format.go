package codec

import (
	"fmt"
	"iter"
	"maps"
	"slices"
	"strings"

	"github.com/daabr/versipellis/pkg/config"
	"github.com/daabr/versipellis/pkg/flow"
)

// Format* constants represent all the available types of "format" settings in the TOML file.
const (
	FormatDefault   config.Format = "default"
	FormatJSON      config.Format = "json"       // JSON object or array, depending on the number of elements.
	FormatJSONArray config.Format = "json_array" // Always an array, even when there are fewer than 2 objects.
	FormatNDJSON    config.Format = "ndjson"     // Newline-delimited JSON (https://ndjson.com/), a.k.a. JSONL.
	FormatRaw       config.Format = "raw"        // Opaque byte payload.
)

var (
	validFormats = []config.Format{
		FormatDefault,

		FormatJSON,
		FormatJSONArray,
		FormatNDJSON,

		FormatRaw,
	}

	structuredFormats = map[config.Format]Formatter{
		FormatJSON:      JSON,
		FormatJSONArray: JSONArray,
		FormatNDJSON:    NDJSON,
	}
)

// ParseFormat returns the normalized [config.Format] setting from the given TOML-based configuration map of a single
// collector, receiver, or sender. It returns [FormatDefault] if the setting is missing, and an error if it's invalid.
func ParseFormat(cfg map[string]any) (config.Format, error) {
	format := strings.ToLower(strings.TrimSpace(config.Value(cfg, "format", string(FormatDefault))))
	if !slices.Contains(validFormats, config.Format(format)) {
		return "", fmt.Errorf("unrecognized value %q", format)
	}
	return config.Format(format), nil
}

// ParseStructuredFormat is like [ParseFormat], but for components that handle only structured data (e.g., JSON
// objects and SQL rows): it returns an error for formats that don't apply to structured data, such as [FormatRaw].
func ParseStructuredFormat(cfg map[string]any) (config.Format, error) {
	format, err := ParseFormat(cfg)
	if err != nil {
		return "", err
	}
	if _, found := structuredFormats[format]; !found && format != FormatDefault {
		return "", fmt.Errorf("structured data doesn't support %q format", format)
	}
	return format, nil
}

// Formatter encodes structured data (e.g., JSON objects and SQL rows) into a single
// byte payload. Instances are immutable, stateless, and safe for concurrent use.
type Formatter interface {
	ContentType() string
	EncodeJSON(objects flow.JSON) ([]byte, error)
	EncodeRows(rows flow.Rows) ([]byte, error)
}

// StructuredFormats returns a read-only and concurrency-safe iterator over each [config.Format] that
// supports structured data (e.g., JSON objects and SQL rows), and its corresponding [Formatter].
func StructuredFormats() iter.Seq2[config.Format, Formatter] {
	return maps.All(structuredFormats)
}

// Resolve returns the preferred [config.Format] if it applies to structured data, or else the fallback format, and
// the corresponding [Formatter]. The fallback must be a structured format (e.g., [FormatJSON] or [FormatNDJSON]),
// otherwise this function panics, because it's a programming error rather than a configuration or data issue.
func Resolve(preferred, fallback config.Format) (config.Format, Formatter) {
	if f, found := structuredFormats[preferred]; found {
		return preferred, f
	}
	if f, found := structuredFormats[fallback]; found {
		return fallback, f
	}
	panic(fmt.Sprintf("codec.Resolve: fallback format %q doesn't apply to structured data", fallback))
}
