package dest

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
)

const (
	redacted = "REDACTED"
)

// sensitiveSubstrings are lowercase fragments of HTTP header and URL query parameter names
// that may carry sensitive details, e.g., "X-Api-Key", "X-Auth-Token", and "access_token".
var sensitiveSubstrings = []string{
	"assertion", "auth", "cert", "cookie", "cred", "csrf", "hmac", "jwt", "key",
	"otp", "pass", "secret", "session", "signature", "ticket", "token", "xsrf",
}

// sensitiveNames are lowercase names that may carry sensitive details,
// but are too short or generic to be matched as substrings.
var sensitiveNames = []string{"code", "dpop", "pwd", "sid", "sig"}

// sensitive reports whether an HTTP header or a URL query parameter with
// the given name may carry sensitive details. It errs on the side of caution.
func sensitive(name string) bool {
	name = strings.ToLower(name)
	if slices.Contains(sensitiveNames, name) {
		return true
	}
	return slices.ContainsFunc(sensitiveSubstrings, func(s string) bool {
		return strings.Contains(name, s)
	})
}

// redactHeaders replaces the values of HTTP headers that may carry sensitive details, in place.
// It assigns new slices instead of modifying existing ones, in case they're shared.
func redactHeaders(h http.Header) {
	for name, vals := range h {
		if !sensitive(name) {
			continue
		}
		newVals := make([]string, len(vals))
		for i, v := range vals {
			newVals[i] = redactValue(name, v)
		}
		h[name] = newVals
	}
}

// redactValue redacts a sensitive HTTP header value, while preserving non-secret parts that
// are useful for debugging: authentication schemes, cookie names, and "Set-Cookie" attributes.
func redactValue(name, value string) string {
	if strings.TrimSpace(value) == "" {
		return value
	}

	switch strings.ToLower(name) {
	case "authorization", "proxy-authorization":
		scheme, _, found := strings.Cut(strings.TrimSpace(value), " ")
		if found && !strings.ContainsAny(scheme, "=:") {
			return scheme + " " + redacted
		}
		return redacted

	case "cookie":
		pairs := strings.Split(value, ";")
		for i, pair := range pairs {
			pairs[i] = redactCookiePair(pair)
		}
		redactedCookie := strings.Join(pairs, "; ")
		if pairs[len(pairs)-1] == "" {
			redactedCookie = strings.TrimSuffix(redactedCookie, " ")
		}
		return redactedCookie

	case "set-cookie":
		pair, attrs, found := strings.Cut(value, ";")
		if found {
			return redactCookiePair(pair) + ";" + attrs
		}
		return redactCookiePair(pair)

	default:
		return redacted
	}
}

// redactCookiePair redacts the value of a single "name=value" cookie pair, keeping its name.
func redactCookiePair(pair string) string {
	pair = strings.TrimSpace(pair)
	if pair == "" {
		return ""
	}
	if name, _, found := strings.Cut(pair, "="); found {
		return name + "=" + redacted
	}
	return redacted
}

// redactQuery redacts the values of URL query parameters that may carry sensitive details. It edits the raw
// query string instead of parsing and re-encoding it, to preserve the order and escaping of all the parameters.
func redactQuery(rawQuery string) string {
	if rawQuery == "" {
		return rawQuery
	}

	params := strings.Split(rawQuery, "&")
	changed := false
	for i, param := range params {
		key, val, found := strings.Cut(param, "=")
		if !found || val == "" || val == redacted {
			continue
		}
		name := key
		if unescaped, err := url.QueryUnescape(key); err == nil {
			name = unescaped
		}
		if sensitive(name) {
			params[i] = key + "=" + redacted
			changed = true
		}
	}

	if !changed {
		return rawQuery
	}
	return strings.Join(params, "&")
}
