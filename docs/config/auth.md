# Versipellis Configuration Reference

## `[*.auth]` Sub-Section

This sub-section can be added to:

- [HTTP](./collector/http.md) and [HTTP/3](./collector/http3.md) collectors
- [HTTP](./sender/http.md) and [HTTP/3](./sender/http3.md) senders

It is entirely optional, but some fields may be required in some use-cases.

`type` - which type of authentication/authorization to use

- Optional
- Default: `"none"`
- Options (case insensitive):
  - `"none"`
  - `"basic"` (username and password, using [RFC 7617](https://datatracker.ietf.org/doc/html/rfc7617), requires both `basic_*` fields - see below)
  - `"static"` (static key or token, requires at least the `static_secret` field - see below)
  - `"oauth"` ([OAuth 2](https://oauth.net/2/) flows, requires at least some `oauth_*` fields - see below)

### Basic ([RFC 7617](https://datatracker.ietf.org/doc/html/rfc7617))

`basic_username` - username for basic authentication

- Required when `type = "basic"`

`basic_password` - password for basic authentication

- Required when `type = "basic"`

### Static Key or Token

`static_header` - name of the HTTP header containing the static key or token

- Optional
- Default: `"Authorization"`
- Case insensitive by definition
- Also affects `"oauth"`-type configurations

`static_prefix` - constant prefix in the HTTP header value before `static_secret`

- Optional
- Default: `"Bearer"`
- Options: any string, including an empty one (`""`)
- If the value isn't empty and doesn't end in a space, we automatically append one to it
- Also affects `"oauth"`-type configurations (appears before the access token)

`static_secret` - the static key or token to use

- Required (only) when `type = "static"`

**Example 1:** static bearer token (based on [RFC 6750](https://datatracker.ietf.org/doc/html/rfc6750), but without any OAuth flow)

- `static_secret = "..."`
- Resulting HTTP header: `Authorization: Bearer ...`

**Example 2:** Splunk HEC (based on [Splunk documentation](https://help.splunk.com/en?resourceId=Splunk_Data_UsetheHTTPEventCollector))

- `static_prefix = "Splunk"`
- `static_secret = "..."`
- Resulting HTTP header: `Authorization: Splunk ...`

**Example 3:** legacy Anthropic API (based on [Claude API documentation](https://platform.claude.com/docs/en/manage-claude/authentication))

- `static_header = "x-api-key"`
- `static_prefix = ""`
- `static_secret = "..."`
- Resulting HTTP header: `X-Api-Key: ...`

### [OAuth 2](https://oauth.net/2/)

`oauth_grant_type` - specific grant type to use in the OAuth flow

- Optional, but it's recommended to set it explicitly
- Dynamic default, based on these ordered rules:
  1. `"authorization_code"` if `oauth_auth_url` _or_ `oauth_callback_url` is set
  2. `"refresh_token"` if `oauth_refresh_token` is set
  3. `"client_credentials"` if `oauth_client_id` is set
  4. `"jwt_bearer"` otherwise
- Options (case insensitive):
  - [`"authorization_code"`](https://oauth.net/2/grant-types/authorization-code/) (3-legged flow requiring one-time user interaction via a local browser)
  - [`"client_credentials"`](https://oauth.net/2/grant-types/client-credentials/)
  - [`"refresh_token"`](https://oauth.net/2/grant-types/refresh-token/)
  - [`"jwt_bearer"`](https://oauth.net/2/jwt/) (normalized to `"urn:ietf:params:oauth:grant-type:jwt-bearer"`)

`oauth_client_id` - client ID for most OAuth flows

- Required when `type = "oauth"`, _unless_ `oauth_grant_type = "jwt_bearer"`

`oauth_client_secret` - client secret for some OAuth flows

- Required when `oauth_grant_type = "authorization_code"`, _unless_ `oauth_pkce = true`
- Also required when `oauth_grant_type = "client_credentials"`

`oauth_auth_url` - authorization endpoint URL for interactive OAuth flows

- Required when `oauth_grant_type = "authorization_code"`
- Must start with `"https://"` (per [RFC 6749 § 3.1](https://datatracker.ietf.org/doc/html/rfc6749#section-3.1)),\
  unless the host is loopback (`localhost`, `127.0.0.0/8`, or `[::1]`)

`oauth_callback_url` - redirection URL back to the client in interactive OAuth flows

- Required when `oauth_grant_type = "authorization_code"`
- Must start with `"https://"` (per [RFC 6749 § 3.1.2.1](https://datatracker.ietf.org/doc/html/rfc6749#section-3.1.2.1)),\
  unless the host is loopback (`localhost`, `127.0.0.0/8`, or `[::1]`)

`oauth_token_url` - token endpoint URL for all OAuth flows

- Required when `type = "oauth"`
- Must start with `"https://"` (per [RFC 6749 § 3.2](https://datatracker.ietf.org/doc/html/rfc6749#section-3.2)),\
  unless the host is loopback (`localhost`, `127.0.0.0/8`, or `[::1]`)

`oauth_refresh_token` - static refresh token

- Required when `oauth_grant_type = "refresh_token"`

`oauth_pkce` (boolean) - use "Proof Key for Code Exchange" ([RFC 7636](https://datatracker.ietf.org/doc/html/rfc7636)) in interactive OAuth 2.x flows

- Optional, effective only when `oauth_grant_type = "authorization_code"`
- Default: `false`
- Options: `true`, `false`
- Must be `true` in interactive OAuth **2.1** flows, but this is not enforced here

`oauth_scopes` - list of (zero or more) permission scopes to request in any OAuth flow

- Optional
- The TOML file format supports these two representation options:

  ```toml
  # Single space-separated string:
  oauth_scopes = "one two ..."

  # Array of strings:
  oauth_scopes = [ "one", "two", ... ]
  ```

`oauth_auth_params` - additional query parameters in `oauth_auth_url`

- Optional, effective only when `oauth_grant_type = "authorization_code"`
- Format: table of string key-value pairs (see also the last note under `jwt_claims` regarding TOML syntax)
- Parameter values must not contain control characters, but may contain spaces (e.g., `prompt = "login consent"`)
- Reserved parameters, which are set by other fields or auto-generated, are rejected: `client_id`, `code_challenge`, `code_challenge_method`, `redirect_uri`, `response_type`, `scope`, and `state`

`oauth_token_params` - additional form parameters in requests to `oauth_token_url`

- Optional, effective only when `oauth_grant_type = "client_credentials"`
- Format: table of string key-value pairs (see also the last note under `jwt_claims` regarding TOML syntax)
- Parameter values must not contain control characters, but may contain spaces
- Reserved parameters, which are set by other fields, are rejected: `client_id`, `client_secret`, and `scope`
- `grant_type` may be overridden, for non-compliant servers

### OAuth 2 - JWT Bearer ([RFC 7523](https://datatracker.ietf.org/doc/html/rfc7523))

`jwt_private_key_file` - relative or absolute path to the private key to sign generated tokens

- Required when `oauth_grant_type = "jwt_bearer"`
- The file must be PEM-encoded and unencrypted
- Supported key types, which determine the signing algorithm:
  - RSA (PKCS #1 or PKCS #8): `RS256`
  - ECDSA (SEC 1 or PKCS #8) with the P-256, P-384, or P-521 curve: `ES256`, `ES384`, or `ES512`, respectively
  - Ed25519 (PKCS #8): `EdDSA`

`jwt_expiration` - maximum age of each generated token

- Optional
- Default: `"5m"` (5 minutes)
- Format: string containing decimal numbers, each with a unit suffix, e.g., `m` (minutes), and `s` (seconds)
- Valid range: between `"60s"` (1 minute) and `"1h"` (1 hour)
- Out-of-bounds values are clamped to the nearest limit

`jwt_claims` - static claims in each generated token

- Required when `oauth_grant_type = "jwt_bearer"`
- Values may be of any TOML type; TOML date-times are converted to Unix timestamps (whole seconds)
  - Local date-times and dates (without a time zone offset) are in the host's local time zone, dates at midnight
  - `exp`, `nbf`, and `iat` must be numbers or date-times, which may also be quoted strings in the same formats
- `exp` and `iat` are ignored, because they're set automatically in each generated token (see `jwt_expiration`)
- The TOML file format supports multiple representation options for key-value pairs:

  ```toml
  [*.auth]
  # ...
  jwt_claims = { iss = "value1", sub = "value2", ... }
  ```

  ```toml
  [*.auth]
  # ...
  jwt_claims = {
      iss = "value1",
      sub = "value2",
      # ...
  }
  ```

  ```toml
  [*.auth]
  # ...
  jwt_claims.iss = "value1"
  jwt_claims.sub = "value2"
  # ...
  ```

  ```toml
  [*.auth]
  # ...

  [*.auth.jwt_claims]
  iss = "value1"
  sub = "value2"
  # ...
  ```
