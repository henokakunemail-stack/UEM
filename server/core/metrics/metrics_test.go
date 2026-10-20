package metrics

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// sampleLine is what the Prometheus text parser accepts for a sample:
// name{labels} value, with names and label names from the allowed alphabet.
var sampleLine = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*(\{([a-zA-Z_][a-zA-Z0-9_]*="([^"\\]|\\.)*",?)*\})? [-+0-9.eEInfNa]+$`)

func scrape(t *testing.T, r *Registry) []string {
	t.Helper()
	var b strings.Builder
	if err := r.Write(&b); err != nil {
		t.Fatal(err)
	}
	var lines []string
	sc := bufio.NewScanner(strings.NewReader(b.String()))
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines
}

// Every line is either a HELP/TYPE comment or a sample the scraper can parse. A
// single malformed line makes Prometheus reject the whole scrape, so this is
// checked line by line.
func TestExpositionIsParseable(t *testing.T) {
	r := NewRegistry()
	c := r.Counter("em_test_total", "A counter with a \"quoted\"\nhelp.", "kind")
	c.Inc(`we"ird\label`)
	c.Add(3, "plain")
	r.GaugeFunc("em_test_gauge", "A gauge.", map[string]string{"node": "a"}, func() float64 { return 2.5 })
	h := r.Histogram("em_test_seconds", "A histogram.", []float64{0.1, 1}, "route")
	h.Observe(0.05, "/x")
	h.Observe(0.5, "/x")
	h.Observe(5, "/x")

	lines := scrape(t, r)
	for _, l := range lines {
		if strings.HasPrefix(l, "# HELP ") || strings.HasPrefix(l, "# TYPE ") {
			continue
		}
		if !sampleLine.MatchString(l) {
			t.Errorf("unparseable line: %q", l)
		}
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{
		`em_test_total{kind="plain"} 3`,
		`em_test_gauge{node="a"} 2.5`,
		`em_test_seconds_bucket{route="/x",le="0.1"} 1`,
		`em_test_seconds_bucket{route="/x",le="1"} 2`,
		`em_test_seconds_bucket{route="/x",le="+Inf"} 3`,
		`em_test_seconds_count{route="/x"} 3`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
}

// Requests are labelled by route pattern, never by raw path: one series per
// device id would grow without bound.
func TestHTTPMetricsUseTheRoutePattern(t *testing.T) {
	reg := NewRegistry()
	m := NewHTTP(reg)
	router := chi.NewRouter()
	router.Use(m.Middleware)
	router.Get("/api/devices/{id}", func(w http.ResponseWriter, r *http.Request) {
		SetUser(r.Context(), "u-1")
		w.WriteHeader(http.StatusTeapot)
	})
	for _, id := range []string{"a", "b", "c"} {
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/devices/"+id, nil))
	}
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/nope", nil))

	joined := strings.Join(scrape(t, reg), "\n")
	if !strings.Contains(joined, `em_http_requests_total{method="GET",route="/api/devices/{id}",status="418"} 3`) {
		t.Fatalf("requests not grouped by route pattern:\n%s", joined)
	}
	if strings.Contains(joined, "/api/devices/a") {
		t.Fatal("a raw path leaked into a label")
	}
	if !strings.Contains(joined, `route="unmatched"`) {
		t.Fatal("an unmatched request was not labelled unmatched")
	}
}

func TestSetUserOutsideARequestIsSafe(t *testing.T) {
	SetUser(context.Background(), "u-1") // must not panic
}
