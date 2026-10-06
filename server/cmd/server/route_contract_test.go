package main

// Route contract tests. These exist because the web console and the Go server
// are two halves of one product with no shared type system: a path typo or a
// renamed JSON field on either side compiles fine and only shows up as a blank
// page in the browser. Every list the console calls is asserted here, and every
// response body is asserted to be a real JSON array (`[]`), never `null` — a
// nil Go slice marshals to `null`, and `null.map(...)` is a React crash.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/config"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/logger"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
)

func init() { logger.Init("disabled", "") }

func testConfig(t *testing.T) config.Config {
	t.Helper()
	dir := t.TempDir()
	return config.Config{
		HTTPAddr:          "127.0.0.1:0",
		DBPath:            filepath.Join(dir, "contract.db"),
		BackupDir:         dir,
		JWTSecret:         "contract-test-secret-contract-test-secret-1234",
		AccessTokenTTL:    time.Hour,
		RefreshTokenTTL:   time.Hour,
		EnrollmentTTL:     time.Hour,
		AgentOfflineAfter: time.Minute,
		BackupInterval:    time.Hour,
		BackupRetain:      1,
		LogLevel:          "disabled",
	}
}

// routesOf builds the production router and returns the set of registered
// "METHOD /pattern" strings, so a console path that was never wired up fails
// here instead of 404-ing silently in a browser.
func routesOf(t *testing.T) map[string]bool {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "routes.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	srv, stopBackground := buildServer(testConfig(t), database)
	t.Cleanup(stopBackground)

	router, ok := srv.Handler.(chi.Routes)
	if !ok {
		t.Fatalf("server handler is %T, not a chi.Routes", srv.Handler)
	}
	return collectRoutes(router)
}

func collectRoutes(r chi.Routes) map[string]bool {
	out := map[string]bool{}
	err := chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		out[method+" "+chiPlaceholder.ReplaceAllString(route, "{}")] = true
		return nil
	})
	if err != nil {
		panic(err)
	}
	return out
}

// chiPlaceholder matches chi's own route placeholders, e.g. the {id} in
// /api/devices/{id}. Normalising these on both sides of the comparison keeps a
// console-side `${deviceId}` and a server-side `{id}` from looking like drift
// when they are the same segment.
var chiPlaceholder = regexp.MustCompile(`\{[a-zA-Z0-9_]+\}`)

// consoleAPIPaths parses every request<...>('/api/...') call out of the real
// web-console/src/services/api.ts and returns them as "METHOD /path", with both
// the console's `${...}` holes and the server's chi `{...}` placeholders
// normalised to a bare `{}` so the two sides can be compared.
//
// It reads the actual file rather than a hardcoded list on purpose: a
// hand-maintained list would just re-encode my own assumptions and could pass
// while the browser called something else.
func consoleAPIPaths(t *testing.T) []string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "..", "..", "web-console", "src", "services", "api.ts"))
	if err != nil {
		t.Fatalf("read web-console/src/services/api.ts: %v", err)
	}

	// Hand-parse `request<T>( 'path'[, { method: 'POST', ... }])` out of the
	// source. Done by hand rather than by regex because Go's RE2 has no
	// backreferences, and because the generic parameter and the options object
	// are easier to read as tokens than as one pattern.
	methodRe := regexp.MustCompile(`method:\s*['"]([A-Z]+)['"]`)
	placeholderRe := regexp.MustCompile(`\$\{[a-zA-Z0-9_]+\}`)
	// A query spliced onto the end of a path: text directly followed by a hole.
	// A path parameter has a '/' in front of it instead, so it does not match.
	querySpliceRe := regexp.MustCompile(`([a-zA-Z0-9_)\]])\$\{[a-zA-Z0-9_.]+\}`)

	var out []string
	seen := map[string]bool{}
	rest := string(src)

	for {
		i := strings.Index(rest, "request<")
		if i < 0 {
			break
		}
		rest = rest[i+len("request<"):]

		// The generic parameter ends at the first '(' — a nested '>' inside
		// the type is possible, so anchor on the paren, not the angle bracket.
		open := strings.Index(rest, "(")
		if open < 0 {
			break
		}
		rest = strings.TrimLeft(rest[open+1:], " \t\r\n")
		if rest == "" {
			break
		}

		quote := rest[0]
		if quote != '\'' && quote != '"' && quote != '`' {
			continue
		}
		// Find the closing quote. A backtick path may contain `${...}`
		// template holes, but none of those holes contain a backtick in
		// api.ts, so the next matching quote is always the real terminator.
		scan := rest[1:]
		end := strings.IndexByte(scan, quote)
		if end < 0 {
			break
		}
		path := scan[:end]
		after := scan[end+1:]

		// A query string is not part of the route: chi matches on the path.
		// `${params.toString()}` is one such case, and leaving it in would make
		// every filtered list look like an unregistered route.
		if q := strings.IndexByte(path, '?'); q >= 0 {
			path = path[:q]
		}
		// A query can also be spliced in whole from a variable
		// (``/api/alerts/incidents${q}``), so a bare `?` is not the only marker.
		// The discriminator is what precedes the hole: a path parameter is
		// always a whole segment (``/devices/${id}``), while a spliced query is
		// always glued to the end of the preceding text.
		path = querySpliceRe.ReplaceAllString(path, "$1")

		// The options object belongs to THIS request only. A fixed window
		// would pick up the next request's `method:` when a call has no
		// options, so scan forward to this call's own closing paren, skipping
		// over any that sit inside a string.
		window := balancedCallArgs(after)
		method := "GET"
		if m := methodRe.FindStringSubmatch(window); m != nil {
			method = m[1]
		}

		rest = after
		if strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/healthz") {
			// Collapse every `${...}` hole to a single token: the variable is a
			// console-side name (`deviceId`, `execId`) with no counterpart in the
			// route pattern, and chi's own placeholder names differ per route.
			// Matching on position, not on the name, is what makes
			// `/api/devices/${deviceId}/executions/${execId}` compare equal to the
			// server's `/api/devices/{id}/executions/{execId}`.
			entry := method + " " + placeholderRe.ReplaceAllString(path, "{}")
			if !seen[entry] {
				seen[entry] = true
				out = append(out, entry)
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("parsed zero API paths from api.ts — the parser is stale, not the console")
	}
	return out
}

// rawFetchPaths are the console's direct `fetch()` calls that never pass
// through the typed `request<T>()` helper, so the parser above cannot see them.
// They are listed explicitly because two of them are the gates the whole
// console depends on, and a route parser that misses them reports clean while
// login is down:
//
//   - /api/auth/login is the only call in api.ts that is allowed to return 401
//     without bouncing the session, so it is the first thing any operator hits.
//   - /api/auth/refresh is what request() itself calls to keep a session alive;
//     a missing route turns every token expiry into a hard re-login.
//   - /healthz is polled by ConsoleShell to decide whether the shell renders at
//     all, and it is the only path here outside /api.
//
// The parser cannot be taught these without also matching every third-party
// fetch in the tree, and a literal list is the smaller and more honest thing:
// it says "these three matter" instead of pretending the file was fully mined.
// Each entry is re-read from the source below, so a rename in api.ts still
// fails here rather than silently drifting from this list.
var rawFetchPaths = []string{
	"/api/auth/login",
	"/api/auth/refresh",
	"/healthz",
}

// TestThePathsTheConsoleFetchesDirectlyStillExist covers the routes above, which
// the typed-call parser structurally cannot reach. /api/auth/refresh appears
// twice in api.ts (request()'s retry and fetchRaw()'s), so the check is that
// each listed path is really still fetched somewhere, not that it appears once.
func TestThePathsTheConsoleFetchesDirectlyStillExist(t *testing.T) {
	routes := routesOf(t)
	src, err := os.ReadFile(filepath.Join("..", "..", "..", "web-console", "src", "services", "api.ts"))
	if err != nil {
		t.Fatalf("read web-console/src/services/api.ts: %v", err)
	}
	// ConsoleShell's /healthz poll lives in a different file; without it the
	// check below would report the health route as console-side dead code.
	shell, err := os.ReadFile(filepath.Join("..", "..", "..", "web-console", "src", "components", "layout", "ConsoleShell.tsx"))
	if err != nil {
		t.Fatalf("read web-console/src/components/layout/ConsoleShell.tsx: %v", err)
	}
	consoleSrc := string(src) + "\n" + string(shell)

	for _, path := range rawFetchPaths {
		t.Run(path, func(t *testing.T) {
			// The literal the console writes between quotes, so a path that
			// moved to a template string is still noticed by a human reading
			// the failure.
			if !strings.Contains(consoleSrc, "'"+path+"'") &&
				!strings.Contains(consoleSrc, `"`+path+`"`) {
				t.Fatalf("the console no longer fetches %s: the raw-fetch list in "+
					"route_contract_test.go is stale, and the route is now unguarded",
					path)
			}
			// Any method the route is registered under satisfies the console,
			// which builds the request itself: /healthz is a GET and both auth
			// routes are POSTs, but the assertion that matters is that the
			// pattern is wired up at all.
			found := false
			for route := range routes {
				if strings.HasSuffix(route, " "+path) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("MISSING ROUTE: %s is fetched by the console but not "+
					"registered on the server", path)
			}
		})
	}
}

func TestEveryConsoleAPIPathIsRegistered(t *testing.T) {
	routes := routesOf(t)
	paths := consoleAPIPaths(t)

	var missing []string
	for _, p := range paths {
		if !routes[p] {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		for _, p := range missing {
			t.Errorf("MISSING ROUTE: %s", p)
		}
		t.Errorf("web console calls %d path(s) the server never registers (of %d parsed from api.ts)",
			len(missing), len(paths))
	}
}

// listEndpoints is every console call whose return value the page feeds
// straight into a `.map()` or a `.length`. On an empty database these are the
// exact requests that used to answer `200 null` and crash React, so they get a
// real HTTP probe rather than a compile-time check.
var listEndpoints = []string{
	"/api/dashboard/sites",
	"/api/dashboard/os",
	"/api/dashboard/alerts",
	"/api/dashboard/activity",
	"/api/devices",
	"/api/audit-logs",
	"/api/software/packages",
	"/api/software/deployments",
	"/api/patches",
	"/api/patches/summary",
	"/api/scripts",
	"/api/schedules",
	"/api/schedules/runs",
	"/api/filter/policies",
	"/api/alerts/rules",
	"/api/alerts/incidents",
	"/api/assets",
	"/api/licenses",
	"/api/licenses/compliance",
	"/api/agent-updates/releases",
	"/api/agent-updates/campaigns",
	"/api/users",
	"/api/maintenance/tasks",
	"/api/maintenance/jobs",
}

// listShapes declares, per endpoint, whether the server answers with a bare
// JSON array or an envelope object, and — for envelopes — the key the console
// destructures to reach the list.
//
// This exists because "the route is registered" and "the body is not null" are
// both necessary and nowhere near sufficient. Two real bugs shipped through
// that gap and passed every other test in this file:
//
//   - GET /api/filter/policies answers {policies, count}; the client typed it
//     as FilterPolicyDTO[], so the page called .some() on an object and blanked.
//   - GET /api/alerts/incidents answers a bare array; the client destructured
//     .incidents off it, got undefined, and showed "no incidents" while the
//     server had them — a wrong answer with no error anywhere.
//
// Both are non-null, valid JSON, so no assertion in this file could see them.
// A shape check can.
var listShapes = []struct {
	path string
	// key is empty when the body is a bare array; otherwise it is the envelope
	// key that must hold the list.
	key string
}{
	{path: "/api/dashboard/sites"},
	{path: "/api/dashboard/os"},
	{path: "/api/dashboard/alerts"},
	{path: "/api/dashboard/activity"},
	{path: "/api/devices", key: "devices"},
	{path: "/api/audit-logs", key: "logs"},
	{path: "/api/software/packages"},
	{path: "/api/software/deployments"},
	{path: "/api/patches"},
	// These three answer with an object, not a list — verified against what the
	// console actually destructures, not against the endpoint's name. A name
	// that sounds like a list ("summary", "compliance") is not evidence of
	// shape; the first draft of this table got all three wrong.
	{path: "/api/patches/summary", key: "total_missing_patches"},
	{path: "/api/scripts"},
	{path: "/api/schedules"},
	{path: "/api/schedules/runs"},
	{path: "/api/filter/policies", key: "policies"},
	{path: "/api/alerts/rules"},
	{path: "/api/alerts/incidents"},
	{path: "/api/assets"},
	{path: "/api/licenses"},
	{path: "/api/licenses/compliance", key: "compliance"},
	{path: "/api/agent-updates/releases"},
	{path: "/api/agent-updates/campaigns"},
	{path: "/api/users"},
	// The parameterised maintenance reads (/api/maintenance/jobs/{id}/progress
	// and .../tasks) are deliberately absent: both tables issue literal HTTP
	// requests, and a literal GET to a {id} path 404s. Their shape comes from
	// the repository's own non-nil slices.
	{path: "/api/maintenance/tasks"},
	{path: "/api/maintenance/jobs"},
	// A directory that has never been synced answers []. Written as a nil
	// slice it would answer null, and the console's PIC dropdown reads
	// `list || []` only because a nil slice is exactly what a Go server sends
	// for a list that was never built.
	{path: "/api/directory/contacts"},
}

func TestListEndpointResponseShape(t *testing.T) {
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "shape.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	cfg := testConfig(t)
	srv, stopBackground := buildServer(cfg, database)
	t.Cleanup(stopBackground)

	ts := httptest.NewServer(srv.Handler)
	t.Cleanup(ts.Close)

	jwtSvc := auth.NewJWTService(cfg.JWTSecret, cfg.AccessTokenTTL, cfg.RefreshTokenTTL)
	tokens, err := jwtSvc.Issue("test-user", "admin", rbac.RoleAdmin)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	for _, spec := range listShapes {
		t.Run(spec.path, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, ts.URL+spec.path, nil)
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			req.Header.Set("Authorization", "Bearer "+tokens.AccessToken)

			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("GET %s: %v", spec.path, err)
			}
			defer res.Body.Close()
			if res.StatusCode != http.StatusOK {
				t.Fatalf("GET %s returned %d, want 200", spec.path, res.StatusCode)
			}

			var body any
			if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
				t.Fatalf("decode %s: %v", spec.path, err)
			}

			if spec.key == "" {
				if _, ok := body.([]any); !ok {
					t.Errorf("GET %s answered %s, want a bare JSON array — the console maps over this directly",
						spec.path, jsonKind(body))
				}
				return
			}

			env, ok := body.(map[string]any)
			if !ok {
				t.Errorf("GET %s answered %s, want an envelope object with a %q key",
					spec.path, jsonKind(body), spec.key)
				return
			}
			if _, ok := env[spec.key]; !ok {
				t.Errorf("GET %s envelope has no %q key (has %v) — the console destructures it and gets undefined",
					spec.path, spec.key, sortedKeys(env))
			}
		})
	}
}

func jsonKind(v any) string {
	switch v.(type) {
	case []any:
		return "an array"
	case map[string]any:
		return "an object"
	case nil:
		return "null"
	default:
		return "a scalar"
	}
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestListEndpointsNeverReturnNull issues a real request against the production
// router with an empty database and asserts no array-shaped body comes back as
// `null`. A nil Go slice marshals to `null`, and `null.map()` is a white screen
// with no stack trace pointing at the cause — this is the regression guard for
// that whole class of failure.
func TestListEndpointsNeverReturnNull(t *testing.T) {
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "nulls.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	cfg := testConfig(t)
	srv, stopBackground := buildServer(cfg, database)
	t.Cleanup(stopBackground)

	ts := httptest.NewServer(srv.Handler)
	t.Cleanup(ts.Close)

	// Authenticate the way the console does, so RBAC passes and the handler
	// reaches its query instead of rejecting the request outright.
	jwtSvc := auth.NewJWTService(cfg.JWTSecret, cfg.AccessTokenTTL, cfg.RefreshTokenTTL)
	tokens, err := jwtSvc.Issue("test-user", "admin", rbac.RoleAdmin)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	for _, path := range listEndpoints {
		t.Run(path, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, ts.URL+path, nil)
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			req.Header.Set("Authorization", "Bearer "+tokens.AccessToken)

			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("GET %s: %v", path, err)
			}
			defer res.Body.Close()

			if res.StatusCode != http.StatusOK {
				// A non-200 means the route is missing or the request is
				// malformed, which TestEveryConsoleAPIPathIsRegistered covers.
				// Skip rather than fail twice for one problem.
				t.Skipf("GET %s returned %d, not a list response", path, res.StatusCode)
			}

			body, err := io.ReadAll(res.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			text := strings.TrimSpace(string(body))
			if text == "null" || text == "" {
				t.Errorf("GET %s returned %q — the console calls .map() on this and would crash", path, text)
			}
		})
	}
}

// objectEndpoints are console calls whose return value is a single object, so
// the "never return null" rule above does not apply — but the response still
// has to carry the fields the page reads. A missing key renders as `undefined`
// in the page rather than a crash, which is exactly the kind of silent contract
// break that is hard to spot by hand, so they get an explicit assertion.
var objectEndpoints = []struct {
	path   string
	fields []string
}{
	// The Log menu renders `lines` and `log_file` directly and shows a
	// placeholder when either is absent.
	{path: "/api/logs", fields: []string{"lines", "log_file", "total"}},
	// The dashboard's eight KPI tiles and its four quick-action links are all
	// driven from this one object; a renamed field blanks a tile.
	{path: "/api/dashboard/summary", fields: []string{
		"total_devices", "online_devices", "offline_devices",
		"sites_count", "retired_devices", "low_disk_alerts",
		"recent_hw_changes_24h",
	}},
}

func TestObjectEndpointsExposeTheirFields(t *testing.T) {
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "objects.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	cfg := testConfig(t)
	srv, stopBackground := buildServer(cfg, database)
	t.Cleanup(stopBackground)

	ts := httptest.NewServer(srv.Handler)
	t.Cleanup(ts.Close)

	jwtSvc := auth.NewJWTService(cfg.JWTSecret, cfg.AccessTokenTTL, cfg.RefreshTokenTTL)
	tokens, err := jwtSvc.Issue("test-user", "admin", rbac.RoleAdmin)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	for _, ep := range objectEndpoints {
		t.Run(ep.path, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, ts.URL+ep.path, nil)
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			req.Header.Set("Authorization", "Bearer "+tokens.AccessToken)

			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("GET %s: %v", ep.path, err)
			}
			defer res.Body.Close()

			if res.StatusCode != http.StatusOK {
				t.Fatalf("GET %s returned %d, want 200", ep.path, res.StatusCode)
			}
			var body map[string]any
			if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
				t.Fatalf("decode %s: %v", ep.path, err)
			}
			for _, f := range ep.fields {
				if _, ok := body[f]; !ok {
					t.Errorf("GET %s response has no %q key — the console reads it and gets undefined", ep.path, f)
				}
			}
		})
	}
}

// TestCampaignStartIsReachable guards the feature that shipped as dead code:
// the route existed and worked, but nothing in the console ever called it, so
// every campaign an operator built sat in `draft` forever. A test cannot prove
// the button is wired, but it does prove the endpoint the button targets is
// still there, and TestEveryConsoleAPIPathIsRegistered covers the other half.
func TestCampaignStartIsReachable(t *testing.T) {
	routes := routesOf(t)
	if !routes["POST /api/agent-updates/campaigns/{}/start"] {
		t.Errorf("POST /api/agent-updates/campaigns/{id} (start campaign) is not registered — " +
			"the console's Start button would 404 and campaigns would never leave `draft`")
	}
}

// balancedCallArgs returns the argument text of a call, up to but not
// including the paren that closes it. Parens inside string literals are
// ignored so a path or body containing one does not end the scan early.
func balancedCallArgs(s string) string {
	depth := 1
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			switch c {
			case '\\':
				i++ // skip the escaped byte
			case quote:
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"', '`':
			quote = c
		case '(', '{', '[':
			depth++
		case ')', '}', ']':
			depth--
			if depth == 0 {
				return s[:i]
			}
		}
	}
	return s
}
