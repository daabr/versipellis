package dest

import (
	"maps"
	"net/http"
	"slices"
	"testing"
)

func TestSensitive(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		want bool
	}{
		{"Authorization", true},
		{"Proxy-Authorization", true},
		{"WWW-Authenticate", true},
		{"Cookie", true},
		{"Set-Cookie", true},
		{"X-Api-Key", true},
		{"X-Auth-Token", true},
		{"X-Amz-Security-Token", true},
		{"X-Hub-Signature-256", true},
		{"X-Csrf-Token", true},
		{"X-Session-Id", true},
		{"access_token", true},
		{"client_secret", true},
		{"password", true},
		{"X-Amz-Credential", true},
		{"sig", true},
		{"CODE", true},
		{"Accept", false},
		{"Content-Type", false},
		{"Keep-Alive", false},
		{"User-Agent", false},
		{"zipcode", false},
		{"page", false},
		{"Accept-Encoding", false},
		{"Cache-Control", false},
		{"Content-Disposition", false},
		{"Content-Length", false},
		{"Strict-Transport-Security", false},
		{"Traceparent", false},
		{"X-Content-Type-Options", false},
		{"X-Request-Id", false},
		{"X-XSS-Protection", false},
		{"assertion", true},
		{"ticket", true},
		{"totp", true},
		{"DPoP", true},
		{"X-Client-Cert", true},
		{"X-GitHub-OTP", true},
		{"X-Shopify-Hmac-Sha256", true},
		{"X-SSL-Cert", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := sensitive(tt.name); got != tt.want {
				t.Errorf("sensitive(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestRedactValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		desc   string
		header string
		value  string
		want   string
	}{
		{"empty", "Authorization", "", ""},
		{"whitespace", "X-Api-Key", " ", " "},
		{"auth_with_scheme", "Authorization", "Basic dXNlcjpwYXNz", "Basic REDACTED"},
		{"auth_with_params", "Authorization", `Digest username="u", response="r"`, "Digest REDACTED"},
		{"auth_without_scheme", "Authorization", "secret", "REDACTED"},
		{"auth_leading_space", "Proxy-Authorization", " Bearer x", "Bearer REDACTED"},
		{"auth_param_not_scheme", "Authorization", "key=secret extra", "REDACTED"},
		{"auth_userinfo_not_scheme", "Authorization", "user:secret realm", "REDACTED"},
		{"auth_tab_separator", "Authorization", "Bearer\tx", "REDACTED"},
		{"auth_lowercase_name", "authorization", "Bearer x", "Bearer REDACTED"},
		{"cookie_single", "Cookie", "sid=1", "sid=REDACTED"},
		{"cookie_multiple", "Cookie", "a=1;b=2; c=", "a=REDACTED; b=REDACTED; c=REDACTED"},
		{"cookie_without_name", "Cookie", "secret", "REDACTED"},
		{"cookie_trailing_semicolon", "Cookie", "a=1;", "a=REDACTED;"},
		{"cookie_trailing_semicolon_space", "Cookie", "a=1; ", "a=REDACTED;"},
		{"cookie_empty_pair", "Cookie", "a=1;;b=2", "a=REDACTED; ; b=REDACTED"},
		{"cookie_only_semicolon", "Cookie", ";", ";"},
		{"set_cookie_attrs", "Set-Cookie", "sid=1; Path=/; Secure", "sid=REDACTED; Path=/; Secure"},
		{"set_cookie_without_attrs", "Set-Cookie", "sid=1", "sid=REDACTED"},
		{"set_cookie_only_attrs", "Set-Cookie", "; Path=/", "; Path=/"},
		{"set_cookie_without_name", "Set-Cookie", "secret; Path=/", "REDACTED; Path=/"},
		{"set_cookie_lowercase_name", "set-cookie", "sid=1; Secure", "sid=REDACTED; Secure"},
		{"other", "X-Auth-Token", "abc", "REDACTED"},
	}
	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			t.Parallel()

			if got := redactValue(tt.header, tt.value); got != tt.want {
				t.Errorf("redactValue(%q, %q) = %q, want %q", tt.header, tt.value, got, tt.want)
			}
		})
	}
}

func TestRedactHeaders(t *testing.T) {
	t.Parallel()

	redactHeaders(nil) // Must not panic, e.g., for requests and responses without trailers.

	h := http.Header{
		"Authorization": {"Bearer x"},
		"Content-Type":  {"text/plain"},
		"Set-Cookie":    {"a=1; Path=/", "b=2"},
		"X-Api-Key":     {"k1", "k2"},
		"X-Empty-Token": {""},
	}
	want := http.Header{
		"Authorization": {"Bearer REDACTED"},
		"Content-Type":  {"text/plain"},
		"Set-Cookie":    {"a=REDACTED; Path=/", "b=REDACTED"},
		"X-Api-Key":     {"REDACTED", "REDACTED"},
		"X-Empty-Token": {""},
	}
	redactHeaders(h)

	if !maps.EqualFunc(h, want, slices.Equal) {
		t.Errorf("redactHeaders() = %v, want %v", h, want)
	}
}

func TestRedactHeadersDoesNotModifySharedSlices(t *testing.T) {
	t.Parallel()

	shared := []string{"secret"}
	h := http.Header{"X-Api-Key": shared}
	redactHeaders(h)

	if got := h.Get("X-Api-Key"); got != redacted {
		t.Errorf("redactHeaders() X-Api-Key = %q, want %q", got, redacted)
	}
	if shared[0] != "secret" {
		t.Errorf("redactHeaders() modified the original slice: %q", shared[0])
	}
}

func TestRedactQuery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		desc  string
		query string
		want  string
	}{
		{"empty", "", ""},
		{"nothing_sensitive", "a=1&b=%20", "a=1&b=%20"},
		{"key_without_value", "token&a=1", "token&a=1"},
		{"empty_value", "token=&a=1", "token=&a=1"},
		{"sensitive", "a=1&access_token=x&b=2", "a=1&access_token=REDACTED&b=2"},
		{"escaped_key", "api%5Fkey=x", "api%5Fkey=REDACTED"},
		{"invalid_escape", "api%zzkey=x", "api%zzkey=REDACTED"},
		{"repeated_keys", "sig=1&sig=2", "sig=REDACTED&sig=REDACTED"},
		{"empty_params", "&&code=x&", "&&code=REDACTED&"},
		{"already_redacted", "a=1&token=REDACTED", "a=1&token=REDACTED"},
		{"plus_in_key", "api+key=x", "api+key=REDACTED"},
		{"exact_name_case", "SIG=x&Sid=y", "SIG=REDACTED&Sid=REDACTED"},
		{"exact_name_substring", "zipcode=1&page=2", "zipcode=1&page=2"},
		{"value_with_equals", "token=a=b&a=1", "token=REDACTED&a=1"},
	}
	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			t.Parallel()

			if got := redactQuery(tt.query); got != tt.want {
				t.Errorf("redactQuery(%q) = %q, want %q", tt.query, got, tt.want)
			}
		})
	}
}
