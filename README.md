# trickyproxy

**Version:** 2.3.0

An HTTP reverse proxy that implements a **read-through / write-back caching pattern** between a primary storage ("target") and one or more read-only data sources ("donors"). When the target doesn't have the requested data, trickyproxy transparently fetches it from a donor and stores it back into the target for future requests.

Includes a specialized **Riak mode** with support for secondary index (2i) queries and virtual namespace ("space") isolation.

---

## Table of Contents

- [How It Works](#how-it-works)
- [Architecture](#architecture)
  - [Package Structure](#package-structure)
  - [Component Diagram](#component-diagram)
  - [Core Types](#core-types)
- [Request Flow](#request-flow)
  - [Default Mode (HTTP)](#default-mode-http)
  - [Riak Mode](#riak-mode)
- [Configuration](#configuration)
  - [CLI Flags](#cli-flags)
  - [Configuration Files](#configuration-files)
- [Installation](#installation)
  - [From Source](#from-source)
  - [Docker](#docker)
- [Retry and Resilience](#retry-and-resilience)
- [TLS and Security](#tls-and-security)
- [Logging](#logging)
- [Extension Points](#extension-points)
- [Error Codes Reference](#error-codes-reference)
- [Known Limitations and Gotchas](#known-limitations-and-gotchas)
- [Testing](#testing)
- [Build and Deploy](#build-and-deploy)

---

## How It Works

trickyproxy sits between clients and two tiers of backend servers:

- **Target** (read/write) — the primary storage server. All incoming requests go here first.
- **Donors** (read-only) — one or more fallback servers. Queried only when the target cannot satisfy the request.

The core behavior is a **two-pass proxy pattern**:

```
Round 1 (cache miss):

  Client --GET-->  trickyproxy --GET--> TARGET
                                        returns 404
                   trickyproxy --GET--> DONOR
                                        returns 200
                   trickyproxy --POST-> TARGET   (store donor's response)
                                        returns 200
  Client <--200--  trickyproxy          (donor's response)


Round 2 (cache hit):

  Client --GET-->  trickyproxy --GET--> TARGET
                                        returns 200
  Client <--200--  trickyproxy          (target's response)
```

After the first request, the data is stored on the target, so subsequent requests are served directly without contacting donors.

---

## Architecture

### Package Structure

```
trickyproxy/
  main.go               # Entry point: CLI flags, logger, config loading, HTTP server
  client_specific.go    # Mode-specific behavior hooks (default vs riak)
  endpoint/
    endpoint.go         # HTTP client abstraction (Instance, Instances pool)
  tests/
    random_scan.go      # Load testing utility
  benchmark.js          # Node.js benchmark script
  Dockerfile            # Multi-stage Docker build
  Makefile              # Build & publish targets (podman + ECR)
```

### Component Diagram

```
                    +-------------------+
                    |     Client        |
                    +--------+----------+
                             |
                             | HTTP Request
                             v
                    +--------+----------+
                    |   trickyproxy     |
                    |  (HTTP server)    |
                    |                   |
                    |  makeHandler()    |
                    |    stopList?----> REJECT (500)
                    |    serveRequest() |
                    +---+----------+---+
                        |          |
               1st try  |          | 2nd try (if target returns 404)
                        v          v
               +--------+--+  +---+---------+
               |   TARGET   |  |   DONORS    |
               | (read/write)|  | (read-only) |
               |  1 instance |  | round-robin |
               +-------------+  | pool        |
                                 +-------------+
```

### Core Types

#### `endpoint.Instance`

Wraps an `http.Client` with endpoint-specific configuration:

| Field | Type | Description |
|-------|------|-------------|
| `readonly` | `bool` | If true, POST/PUT/PATCH/DELETE are rejected |
| `protocol` | `string` | `"http"` or `"https"` |
| `host` | `string` | Endpoint hostname |
| `port` | `string` | Endpoint port |
| `auth` | `string` | Base64-encoded Basic auth credentials |
| `urlEncoder` | `URLModifier` | Rewrites outgoing URL paths |
| `headerEncoder` | `HeaderModifier` | Rewrites outgoing request headers |
| `headerDecoder` | `HeaderModifier` | Rewrites incoming response headers |
| `client` | `*http.Client` | Configured HTTP client (timeout set via `-donor-timeout` / `-target-timeout`) |

**Key methods:**
- `New(host, port, protocol, auth, urlEncoder, headerEncoder, headerDecoder)` — creates a plain HTTP instance
- `NewTLS(protocol, host, port, auth, keyfile, crtfile)` — creates a TLS-enabled instance with client certificates
- `MakeReadOnly()` — marks the instance as read-only (blocks write methods)
- `Do(req)` — executes the request with URL/header rewriting, body buffering, and up to 10 retries
- `Get(path)` / `Post(path, headers, body)` — convenience wrappers

#### `endpoint.Instances`

Thread-safe round-robin pool of `Instance` objects:

- `NewInstances()` — creates an empty pool
- `Add(inst)` — appends an instance (mutex-protected)
- `Next()` — returns the next instance in round-robin order (mutex-protected)

#### `endpoint.URLModifier`

```go
type URLModifier func(text string) string
```

Function type for rewriting URL paths. Used by Riak mode to inject/strip virtual namespace prefixes.

#### `endpoint.HeaderModifier`

```go
type HeaderModifier func(h http.Header) http.Header
```

Function type for rewriting HTTP headers. In Riak mode, specifically rewrites `Link` header values.

---

## Request Flow

### Default Mode (HTTP)

1. **Stoplist check** — If the request URL matches any regex in `stoplist.conf`, respond with HTTP 500 `URL_IN_STOP_LIST`.

2. **Target request** — Forward the request to the target. If `target.Do()` fails, respond with HTTP 500 `TARGET_DO_METHOD`.

3. **Proxy pass decision** — Evaluate whether to fall back to donors:
   - `isNeedProxyPass()` returns `true` if target responded with **404** and method is **GET** or **HEAD**.
   - `exceptions` regex list from `noproxy.conf` can override this — if the URL matches, no proxy pass occurs.

4. **Donor request** — If proxy pass is needed, fetch from the next donor (round-robin). On donor failure, retry up to 3 times with different donors.

5. **Post-processing** — After a successful donor response:
   - **GET with 200**: Store the donor's response body into the target via POST.
   - **HEAD with 200**: Retrieve the full key from the donor and store it (HEAD cannot carry a body).

6. **Response** — Return the final response (from target or donor) to the client.

### Riak Mode

Activated with `-mode riak` (the default). Extends the default flow with:

#### Virtual Spaces

When `target.conf` includes a `vspace` (e.g., `8.8.8.8:8098:db1`), URL paths and `Link` headers are rewritten to include a namespace prefix, isolating data within the target.

#### Secondary Index (2i) Handling

For GET requests to Riak 2i endpoints (`/buckets/*/index/*`):

1. If the target returns 200 but with **zero keys** in the JSON response (`{"keys": []}`), the proxy treats this as a cache miss and queries donors.
2. When the donor returns keys via 2i, the proxy:
   - Parses the JSON response to extract all key names.
   - For each key, fetches the full value from the donor (`/riak/<bucket>/<key>`).
   - Stores each key-value pair into the target.
3. The original 2i response from the donor is returned to the client.

---

## Configuration

### CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-key` | `certs/service.key` | Path to TLS private key file (for donor connections) |
| `-cert` | `certs/service.pem` | Path to TLS public certificate file (for donor connections) |
| `-donors` | `donors.conf` | Path to donors host list file **(required)** |
| `-target` | `target.conf` | Path to target host file **(required)** |
| `-srvaddr` | `srvaddr.conf` | Path to server listen address file **(required)** |
| `-noproxy` | `noproxy.conf` | Path to proxy exception regexes file (optional) |
| `-stoplist` | `stoplist.conf` | Path to request blocklist regexes file (optional) |
| `-mode` | `riak` | Proxy mode: `riak` or `http` |
| `-logformat` | `json` | Log format: `json` or `console` |
| `-donor-timeout` | `4s` | Timeout for HTTP requests to donor upstreams (e.g. `2s`, `500ms`, `1m`) |
| `-target-timeout` | `4s` | Timeout for HTTP requests to the target upstream |
| `-negative-cache-ttl` | `10m` | TTL for in-memory negative (404) cache for donor responses. Set to `0` to disable. Only GET/HEAD requests are cached; 2i paths (`/index/`) are never cached. |

Special command: `trickyproxy version` prints the version and exits.

### New Configuration Options

#### Configurable Timeouts

By default, both donor and target connections use a **4-second timeout**. Use the `-donor-timeout` and `-target-timeout` flags to override:

```bash
# Increase donor timeout for slow sandbox upstreams
trickyproxy -donor-timeout 10s -target-timeout 2s ...

# Sub-second precision
trickyproxy -donor-timeout 500ms ...
```

Values use Go duration syntax: `300ms`, `2s`, `1m30s`.

#### Negative 404 Cache

When enabled (default TTL: **10 minutes**), trickyproxy caches 404 responses from donors in memory. This prevents repeated donor round-trips for keys that don't exist in any donor.

**What is cached:**
- GET and HEAD requests that return 404 from the donor
- Only plain key paths (2i `/index/` paths are **never** cached)

**What is NOT cached:**
- POST, PUT, DELETE requests
- 5xx responses or network errors
- Riak secondary index (`/index/`) paths

```bash
# Default (10 minute TTL)
trickyproxy -negative-cache-ttl 10m ...

# Shorter TTL for volatile data
trickyproxy -negative-cache-ttl 30s ...

# Disable entirely
trickyproxy -negative-cache-ttl 0 ...
```

### Configuration Files

#### `donors.conf` (required)

Newline-separated list of donor upstream servers. Each line:

```
[protocol://]host:port[:base64_auth]
```

- **protocol** — Optional. `http` or `https` (default: `https`).
- **host:port** — Required. The donor's address.
- **base64_auth** — Optional. Base64-encoded credentials for Basic authentication.

```conf
# Examples
8.8.8.8:8098
8.8.8.9:8098
https://8.8.8.7:8098
somegateway.com:443:bG9naW46cGFzcwo=
```

All donors are marked **read-only** — write methods (POST, PUT, PATCH, DELETE) are rejected.

#### `target.conf` (required)

Single-line target upstream server:

```
host:port[:vspace]
```

- **vspace** — Optional. Virtual space name for Riak namespace isolation. Appended with `_` suffix to create URL prefixes.

```conf
# Without virtual space
8.8.8.8:8098

# With virtual space "db1" (paths rewritten: /riak/bucket/ -> /riak/bucket/db1_)
8.8.8.8:8098:db1
```

The target always uses plain HTTP protocol.

#### `srvaddr.conf` (required)

Single-line listen address for the proxy server:

```
host:port
```

```conf
0.0.0.0:8036
```

#### `noproxy.conf` (optional)

Newline-separated regular expressions. Requests matching any pattern will **not** be proxied to donors (target response is always returned as-is).

```conf
^/health
^/status
^/riak/internal/
```

#### `stoplist.conf` (optional)

Newline-separated regular expressions. Requests matching any pattern are **blocked entirely** with HTTP 500 `URL_IN_STOP_LIST`.

```conf
^/admin/
^/riak/dangerous_bucket/
```

---

## Installation

### From Source

```bash
go get -u github.com/kzub/trickyproxy
go install github.com/kzub/trickyproxy
```

Or clone and build:

```bash
git clone https://github.com/kzub/trickyproxy.git
cd trickyproxy
go build -o trickyproxy
```

### Docker

```bash
docker build -t trickyproxy .
docker run trickyproxy -donors donors.conf -target target.conf -srvaddr srvaddr.conf
```

The Dockerfile uses a multi-stage build:
1. **Build stage** — `golang:1.18-alpine`, compiles a static binary (`CGO_ENABLED=0`).
2. **Runtime stage** — `gcr.io/distroless/base-debian10`, minimal attack surface.

---

## Retry and Resilience

trickyproxy implements retries at two levels:

### Level 1: HTTP Client Retries (endpoint.Do)

When an HTTP request to any upstream fails (network error, timeout, etc.):

- **Retries:** Up to 10 attempts
- **Delay:** 500ms between retries (fixed, no exponential backoff)
- **Body buffering:** Request body is read into memory before the first attempt, allowing reuse across retries
- **Timeout:** Each individual request has a configurable timeout (default: **4s**), set via `-donor-timeout` or `-target-timeout` flags

### Level 2: Donor Rotation Retries (serveRequest)

When a donor request fails after exhausting Level 1 retries:

- **Retries:** Up to 3 attempts
- **Strategy:** Each retry selects the next donor from the round-robin pool
- **Behavior:** Returns `servRetry` which triggers the outer loop to try a different donor

### Effective Worst Case

A single client request can trigger up to **3 donor attempts x 10 retries = 30 HTTP requests** to donors (plus the initial target request with its own 10 retries).

---

## TLS and Security

### Client Certificate Authentication

Donors configured with `https` protocol use mTLS (mutual TLS):

- Certificate and key files are specified via `-cert` and `-key` flags.
- The same cert/key pair is used for all TLS donor connections.
- `tls.LoadX509KeyPair()` is called during donor setup.

### Security Considerations

| Aspect | Behavior | Risk |
|--------|----------|------|
| **Certificate validation** | `InsecureSkipVerify: true` | Server certificates are NOT validated. Suitable for internal networks only. |
| **Cert loading failure** | Logged as error, instance still created | Silent misconfiguration — the donor instance may fail at request time with unclear TLS errors. |
| **Auth credentials** | Stored as Base64 in `donors.conf` | Not encrypted. Protect the config file with filesystem permissions. |
| **Stoplist bypass** | Regexes checked on `r.URL` only | Does not account for URL normalization attacks. |

---

## Logging

Uses [zap](https://github.com/uber-go/zap) structured logger with the following configuration:

| Setting | Value |
|---------|-------|
| Level | `Info` |
| Format | `json` (default) or `console` via `-logformat` |
| Output | `stdout` |
| Sampling | 100 initial, 100 thereafter |
| Timestamp | ISO 8601 (`@timestamp` field) |

### Log Fields

| Field | Description |
|-------|-------------|
| `@timestamp` | ISO 8601 timestamp |
| `level` | Log level (`info`, `error`) |
| `message` | Event description |
| `url` | Request URL |
| `status` | HTTP response status |
| `responseTime` | Response duration in milliseconds |
| `host` | Upstream host |
| `error` | Error message (on error logs) |
| `body` | Response body (logged only for 5xx responses) |

### Key Log Events

| Message | Meaning |
|---------|---------|
| `adding donor upstream` | Donor configured during startup |
| `adding target upstream` | Target configured during startup |
| `server ready` | HTTP server started and listening |
| `fetch donor` | Target returned miss; fetching from donor |
| `cli response` | Response sent to client |
| `store status` | Result of storing donor data to target |
| `RETRIEVE KEY >>>>` | Fetching individual key (Riak 2i mode) |
| `GOT 2i KEYS` | Riak 2i query returned keys to backfill |
| `skip donor request in case of` | Request matched noproxy exception |

---

## Extension Points

trickyproxy uses **function-variable hooks** (not Go interfaces) to allow behavior customization. These are set at startup before the HTTP server starts.

### Hook Variables (in `client_specific.go`)

| Variable | Signature | Purpose |
|----------|-----------|---------|
| `isNeedProxyPass` | `func(resp, r, body) bool` | Decides if target response should trigger donor fallback |
| `postProcess` | `func(donor, target, resp, r, body) (bool, error)` | Post-donor-fetch processing; returns whether to store result |
| `urlEncoder` | `func(space) URLModifier` | Factory returning URL path rewriter for the target |
| `headerEncoder` | `func(space) HeaderModifier` | Factory returning outgoing header rewriter for the target |
| `headerDecoder` | `func(space) HeaderModifier` | Factory returning response header rewriter for the target |

### Adding a New Proxy Mode

1. Create a `setMyMode()` function in `client_specific.go`:

```go
func setMyMode() {
    isNeedProxyPass = func(resp *http.Response, r *http.Request, body []byte) bool {
        // Your logic to determine when to fall back to donors
        return resp.StatusCode == http.StatusNotFound
    }
    postProcess = func(donor, target *endpoint.Instance, resp *http.Response, r *http.Request, body []byte) (bool, error) {
        // Your post-processing logic
        return resp.StatusCode == http.StatusOK, nil
    }
    urlEncoder = urlNoEncoder
    headerEncoder = headerNoEncoder
    headerDecoder = headerNoEncoder
}
```

2. Register the mode in `main.go`:

```go
if *proxmod == "mymode" {
    setMyMode()
}
```

3. Run with `-mode mymode`.

---

## Error Codes Reference

These error category strings appear in HTTP 500 responses and log messages:

| Error Code | Location | Meaning |
|------------|----------|---------|
| `URL_IN_STOP_LIST` | `makeHandler` | Request URL matched a stoplist regex |
| `TARGET_DO_METHOD` | `serveRequest` | Failed to reach the target server |
| `DONOR_DO` | `serveRequest` | Failed to reach any donor after all retries |
| `POST_PROCESS` | `serveRequest` | Error during post-processing (e.g., Riak 2i parsing) |
| `TARGET_STORE` | `serveRequest` | Failed to store donor response back to target |
| `TARGET_GET_KEY` | `retrieveKey` | Failed to GET a key from target |
| `DONOR_GET_KEY` | `retrieveKey` | Failed to GET a key from donor |
| `TARGET_WRITE_KEY` | `retrieveKey` | Failed to POST/store a key to target |
| `CANNOT WRITE TO READONLY ENDPOINT` | `endpoint.Do` | Write method called on a read-only donor |
| `RQ_READ_BODY` | `endpoint.Do` | Failed to read request body |
| `RESP_READ_BODY` | `endpoint.Do` | Failed to read response body |
| `DO_FAILED` | `endpoint.Do` | All 10 retries exhausted for a request |
| `BUCKET_NOT_FOUND` | `get2iBucket` | Could not parse bucket from Riak 2i URL |

---

## Known Limitations and Gotchas

### Empty Donor Pool Panic

If `donors.conf` is empty or contains no valid entries, calling `Instances.Next()` will panic with an index-out-of-range error. Always ensure at least one donor is configured.

### Request Body Memory Usage

For retry support, the entire request body is read into memory before forwarding. Large request bodies will consume proportional memory. There is no size limit or streaming mode.

### No Graceful Shutdown

The HTTP server uses `http.ListenAndServe` without a shutdown mechanism. The process must be terminated with a signal (SIGTERM/SIGKILL). In-flight requests may be dropped.

### TLS Certificate Failures Are Non-Fatal

If the TLS certificate/key files cannot be loaded, the error is logged but the donor instance is still created. This can lead to confusing TLS handshake failures at runtime rather than a clean startup error.

### Fixed Retry Timing

Retries use a fixed 500ms delay with no jitter or exponential backoff. Under high load with upstream failures, this can create retry storms.

### No HTTP/2, WebSocket, or gRPC Support

trickyproxy handles plain HTTP/1.1 requests only. WebSocket upgrades, HTTP/2, and gRPC are not supported.

### No Health Check Endpoint

There is no built-in health or readiness endpoint. Add one to `noproxy.conf` exceptions if you run your own.

### Virtual Space Regex Contains Non-ASCII

The Riak URL encoder regex in `riakURLEncoder` contains a non-ASCII soft-hyphen character (`\u00AD`) in the character class `[^/\u00AD]`. This is intentional for Riak compatibility but may cause confusion when reading the source code.

---

## Testing

### Load Test (Go)

```bash
cd tests
go run random_scan.go
```

Fires 1000 concurrent GET requests to `http://127.0.0.1:8036/riak/test/key{N}` with up to 50 in-flight goroutines.

### Benchmark (Node.js)

```bash
npm install request async
node benchmark.js
```

Sends 1000 requests with concurrency of 50 to `http://localhost:8037/riak/test/key{N}` and reports status code distribution.

### Unit Tests

No unit tests exist in the repository. Key areas that would benefit from test coverage:

- `endpoint.Instances.Next()` round-robin behavior
- `endpoint.Instance.Do()` retry logic
- `isNeedProxyPass` / `isNeedProxyPassRiak` decision logic
- URL/header encoding and decoding
- `getKeysFrom2iResponse` JSON parsing
- `buildRegexpFromPath` regex compilation and matching
- `negativecache.Cache` TTL expiry and concurrent access

---

## Build and Deploy

### Local Build

```bash
go build -o trickyproxy
```

### Docker Build

```bash
# Build
podman build -t trickyproxy .

# Or using Make
make build
```

### Publish to ECR

```bash
make publish
```

Requires AWS credentials (profile `solar`) and pushes to the configured ECR registry. The image tag is derived from the git branch and commit SHA.

### Version

```bash
./trickyproxy version
# version 2.3.0
```
