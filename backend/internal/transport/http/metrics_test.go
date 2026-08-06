package http

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"
	"go.uber.org/zap"

	"qomranote/backend/internal/config"
)

// seriesMatching returns the exposition lines of one metric family whose labels
// all appear on the line, so a test can name the exact series it means.
func seriesMatching(body, family string, labels ...string) []string {
	var out []string
line:
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, family+"{") {
			continue
		}
		for _, label := range labels {
			if !strings.Contains(line, label) {
				continue line
			}
		}
		out = append(out, line)
	}
	return out
}

// exposition returns the text the metrics handler serves right now.
func exposition(t *testing.T, e *echo.Echo) string {
	t.Helper()

	out := httptest.NewRecorder()
	e.ServeHTTP(out, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if out.Code != http.StatusOK {
		t.Fatalf("scrape status = %d, want 200", out.Code)
	}
	return out.Body.String()
}

// scrape drives one request through the metrics middleware and returns the
// exposition text the handler would serve afterwards.
func scrape(t *testing.T, e *echo.Echo, method, target string) string {
	t.Helper()

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(method, target, nil))

	return exposition(t, e)
}

// seriesIn counts the exposition lines belonging to one metric family, i.e. the
// number of distinct label combinations that family currently carries.
func seriesIn(body, family string) int {
	n := 0
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, family+"{") {
			n++
		}
	}
	return n
}

func metricsEcho() *echo.Echo {
	e := echo.New()
	e.HideBanner, e.HidePort = true, true
	e.Use(MetricsMiddleware())
	e.GET("/metrics", MetricsHandler())
	e.GET("/boards/:id", func(c echo.Context) error { return c.NoContent(http.StatusOK) })
	return e
}

func TestMetrics(t *testing.T) {
	t.Run("a request is counted under its route template, not its URL", func(t *testing.T) {
		body := scrape(t, metricsEcho(), http.MethodGet, "/boards/abc123")

		if !strings.Contains(body, `qomranote_http_requests_total{`) {
			t.Fatalf("no request counter in exposition:\n%s", body)
		}
		if !strings.Contains(body, `route="/boards/:id"`) {
			t.Errorf("route template missing from labels:\n%s", body)
		}
		if strings.Contains(body, "abc123") {
			t.Errorf("concrete path leaked into a label, cardinality is unbounded:\n%s", body)
		}
	})

	t.Run("duration is observed as a histogram", func(t *testing.T) {
		body := scrape(t, metricsEcho(), http.MethodGet, "/boards/abc123")

		if !strings.Contains(body, "qomranote_http_request_duration_seconds_bucket{") {
			t.Errorf("no duration histogram in exposition:\n%s", body)
		}
	})

	t.Run("an unmatched path does not mint a series of its own", func(t *testing.T) {
		body := scrape(t, metricsEcho(), http.MethodGet, "/no/such/route")

		if strings.Contains(body, "/no/such/route") {
			t.Errorf("404 path became a label value:\n%s", body)
		}
		if !strings.Contains(body, `route="unmatched"`) {
			t.Errorf("404 was not folded into the unmatched route:\n%s", body)
		}
	})

	// The Host header is client-supplied and unbounded, and nginx forwards it
	// verbatim, so it must reach no label at all.
	t.Run("hostile Host headers mint no series", func(t *testing.T) {
		const marker = "mintaseries.example"
		const family = "qomranote_http_requests_total"

		e := echo.New()
		e.HideBanner, e.HidePort = true, true
		e.Use(MetricsMiddleware())
		e.GET("/metrics", MetricsHandler())
		e.GET("/cardinality/:id", func(c echo.Context) error { return c.NoContent(http.StatusOK) })

		drive := func(host string) {
			req := httptest.NewRequest(http.MethodGet, "/cardinality/abc123", nil)
			req.Host = host
			e.ServeHTTP(httptest.NewRecorder(), req)
		}

		// One benign request plus two scrapes settle every series this Echo can
		// produce, so any later growth is attributable to the Host header alone.
		drive("qomranote.internal")
		exposition(t, e)
		before := seriesIn(exposition(t, e), family)

		longHost := strings.Repeat("a", 4000) + "." + marker
		for i := range 50 {
			drive(fmt.Sprintf("attacker-%d.%s", i, marker))
		}
		drive(longHost)

		body := exposition(t, e)
		if after := seriesIn(body, family); after != before {
			t.Errorf("51 distinct Host values grew %s from %d to %d series", family, before, after)
		}

		matched := 0
		for _, line := range strings.Split(body, "\n") {
			if strings.HasPrefix(line, family+"{") && strings.Contains(line, `route="/cardinality/:id"`) {
				matched++
			}
		}
		if matched != 1 {
			t.Errorf("want exactly 1 series per (route, method, code), got %d:\n%s", matched, body)
		}

		if strings.Contains(body, marker) || strings.Contains(body, strings.Repeat("a", 64)) {
			t.Errorf("a Host value reached the exposition:\n%s", body)
		}
		if strings.Contains(body, "host=") {
			t.Errorf("a host label survives in the exposition:\n%s", body)
		}
	})

	// The request method is a client-chosen token that reaches the middleware
	// before auth or the rate limiter, so only the routed verb set may label it.
	t.Run("hostile request methods mint no series", func(t *testing.T) {
		const marker = "mintaverb"
		const family = "qomranote_http_requests_total"

		e := echo.New()
		e.HideBanner, e.HidePort = true, true
		e.Use(MetricsMiddleware())
		e.GET("/metrics", MetricsHandler())
		e.GET("/cardinality/:id", func(c echo.Context) error { return c.NoContent(http.StatusOK) })

		drive := func(method string) {
			e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(method, "/cardinality/abc123", nil))
		}

		// One benign request, one hostile method and two scrapes settle every
		// series this Echo can produce, so later growth is attributable to the
		// spelling of the method alone.
		drive(http.MethodGet)
		drive("SETTLE" + marker)
		exposition(t, e)
		before := seriesIn(exposition(t, e), family)

		// Real verbs in the wrong case are distinct strings and must not be
		// normalised into the label; the rest are tokens Go's request parser
		// accepts verbatim.
		hostile := []string{
			"get", "post", "put", "patch", "delete", "head", "options", "trace",
			"Get", "pOsT", "DeLeTe", "gEt",
			"!#$%&'*+-.^_`|~", "a!b#c$d%e&f'g*h+i-j.k^l_m`n|o~p",
			strings.Repeat("Z", 4000) + marker,
		}
		for i := range 50 {
			hostile = append(hostile, fmt.Sprintf("ATTACK-%d.%s", i, marker))
		}
		for _, method := range hostile {
			drive(method)
		}

		body := exposition(t, e)
		if after := seriesIn(body, family); after != before {
			t.Errorf("%d hostile methods grew %s from %d to %d series", len(hostile), family, before, after)
		}

		folded := 0
		for _, line := range strings.Split(body, "\n") {
			if strings.HasPrefix(line, family+"{") && strings.Contains(line, `method="other"`) {
				folded++
			}
		}
		if folded != 1 {
			t.Errorf("want every unrouted verb on one series, got %d:\n%s", folded, body)
		}

		if strings.Contains(body, marker) || strings.Contains(body, strings.Repeat("Z", 64)) {
			t.Errorf("a client method token reached the exposition:\n%s", body)
		}
		for _, lowered := range []string{`method="get"`, `method="post"`, `method="pOsT"`} {
			if strings.Contains(body, lowered) {
				t.Errorf("%s survives as a label value:\n%s", lowered, body)
			}
		}
	})

	// Recover is registered OUTSIDE the metrics middleware, so a panic unwinds
	// through it. Recording inline left the resulting 500 in no series at all.
	t.Run("a panicking handler is counted, as a 500", func(t *testing.T) {
		const family = "qomranote_http_requests_total"

		e := echo.New()
		e.HideBanner, e.HidePort = true, true
		e.Logger.SetOutput(io.Discard) // the recovery logs a stack trace
		e.Use(echomw.Recover())
		e.Use(MetricsMiddleware())
		e.GET("/metrics", MetricsHandler())
		e.GET("/panics/:id", func(c echo.Context) error { panic("handler exploded") })

		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panics/abc123", nil))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("recovered panic answered %d, want 500", rec.Code)
		}

		body := exposition(t, e)
		counted := seriesMatching(body, family, `route="/panics/:id"`)
		if len(counted) == 0 {
			t.Fatalf("a panicking request produced no series at all:\n%s", body)
		}
		if len(counted) != 1 {
			t.Errorf("want one series for the panicking route, got %d:\n%s", len(counted), strings.Join(counted, "\n"))
		}
		// The response was never committed to a status and no error was
		// returned, so nothing about the request says 500 except the unwinding
		// itself; counting it as the 200 Echo initialises the response to would
		// be worse than not counting it.
		if !strings.Contains(counted[0], `code="500"`) {
			t.Errorf("panicking request counted as %s, want code=\"500\"", counted[0])
		}
		if len(seriesMatching(body, "qomranote_http_request_duration_seconds_count", `route="/panics/:id"`)) == 0 {
			t.Errorf("the panicking request was counted but not timed:\n%s", body)
		}
	})

	// CORS answers a preflight itself and returns; anything registered after it
	// never runs. Driven through the real NewServer chain, because the ordering
	// of that chain is the whole of what this asserts.
	t.Run("a CORS preflight through the real chain is counted", func(t *testing.T) {
		const family = "qomranote_http_requests_total"
		const origin = "https://app.example"

		srv := NewServer(&config.Config{CORSOrigins: origin}, zap.NewNop(), &Handlers{})

		req := httptest.NewRequest(http.MethodOptions, "/api/v1/boards/abc123", nil)
		req.Header.Set(echo.HeaderOrigin, origin)
		req.Header.Set(echo.HeaderAccessControlRequestMethod, http.MethodPatch)
		rec := httptest.NewRecorder()
		srv.echo.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("preflight answered %d, want 204 — CORS did not short-circuit", rec.Code)
		}
		if rec.Header().Get(echo.HeaderAccessControlAllowOrigin) != origin {
			t.Fatalf("preflight was not answered by the CORS middleware: %v", rec.Header())
		}

		body := exposition(t, srv.echo)
		counted := seriesMatching(body, family, `method="OPTIONS"`)
		if len(counted) == 0 {
			t.Fatalf("a short-circuited preflight reached no series:\n%s", body)
		}
		if !strings.Contains(counted[0], `code="204"`) {
			t.Errorf("preflight counted as %s, want code=\"204\"", counted[0])
		}
		if strings.Contains(body, "abc123") {
			t.Errorf("the preflight's concrete path leaked into a label:\n%s", body)
		}
	})

	// Every Echo built above calls MetricsMiddleware again; registering per call
	// would have panicked on the second one long before here.
	t.Run("the collectors are registered once for the process", func(t *testing.T) {
		first, second := appMetrics(), appMetrics()
		if first.registry != second.registry {
			t.Fatal("registry is rebuilt per call")
		}
		if got := scrape(t, metricsEcho(), http.MethodGet, "/boards/abc123"); !strings.Contains(got, "go_goroutines") {
			t.Errorf("runtime collectors missing from the shared registry:\n%s", got)
		}
	})
}
