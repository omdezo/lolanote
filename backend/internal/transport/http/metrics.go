package http

import (
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Series are namespaced so a scrape that also covers sidecars stays attributable.
const (
	metricsNamespace = "qomranote"
	metricsSubsystem = "http"
)

// routeUnmatched is the route label for a request the router placed nowhere at
// all.
//
// It is narrower than "every 404". A path under a group prefix carries that
// group's auto-registered RouteNotFound template instead, so an unrouted
// /api/v1/… is labelled "/api/v1/*" and an unrouted /api/v1/agent/… is labelled
// "/api/v1/agent/*"; only a path outside every group prefix reaches this
// constant. What matters is that all three folds are strings this server wrote:
// every label value here must come from a bounded set, and the middleware sits
// ahead of auth and of the rate limiter, so an unauthenticated caller decides
// both how many distinct URLs it asks for and how many it is allowed to ask for.
const routeUnmatched = "unmatched"

// methodOther is the method label for a verb this server does not route.
//
// A label value must come from a set this server defines, never from the wire.
// The request method is a client-chosen token, and Echo settles 405 before auth
// or the rate limiter run, so a caller that invents verbs would otherwise mint a
// counter series and a full histogram per spelling it tries.
const methodOther = "other"

// routedMethods is every verb Echo can route (RFC 9110), mapped to the copy of
// itself that reaches the label.
//
// The match is exact: "get" is a distinct string from "GET", so case variants
// fall through to methodOther rather than being folded by normalising client
// input, which would put an unbounded transformation of the wire back in the
// label. The stored copy is what the lookup yields, so the registry retains no
// string that arrived on a connection.
var routedMethods = map[string]string{
	http.MethodGet:     http.MethodGet,
	http.MethodHead:    http.MethodHead,
	http.MethodPost:    http.MethodPost,
	http.MethodPut:     http.MethodPut,
	http.MethodPatch:   http.MethodPatch,
	http.MethodDelete:  http.MethodDelete,
	http.MethodOptions: http.MethodOptions,
	http.MethodConnect: http.MethodConnect,
	http.MethodTrace:   http.MethodTrace,
}

// methodLabel bounds the method label to the routed verb set.
func methodLabel(method string) string {
	if routed, ok := routedMethods[method]; ok {
		return routed
	}
	return methodOther
}

// metricsLabels is the whole label set, and nothing may be added to it that a
// client controls. In particular there is no host label: the Host header is
// client-supplied, unbounded in both value and length, and nginx forwards it
// verbatim, so labelling by it lets any caller mint series at will.
var metricsLabels = []string{"code", "method", "route"}

// Metrics owns the collector registry for this process.
//
// A Prometheus collector may be registered exactly once per registry — a
// duplicate panics — while an Echo instance is built more than once per process
// (NewServer, and every test that stands up its own router). So the registry and
// everything on it are created behind metricsOnce and shared, and the registry is
// ours rather than prometheus.DefaultRegisterer so an unrelated package's
// init-time registration cannot collide with it.
type Metrics struct {
	registry *prometheus.Registry
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

var (
	metricsOnce sync.Once
	metrics     *Metrics
)

// appMetrics returns the process-wide Metrics, building it on first use.
func appMetrics() *Metrics {
	metricsOnce.Do(func() {
		reg := prometheus.NewRegistry()
		reg.MustRegister(
			collectors.NewGoCollector(),
			collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		)

		requests := prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: metricsNamespace,
				Subsystem: metricsSubsystem,
				Name:      "requests_total",
				Help:      "How many HTTP requests processed, partitioned by status code, method and route template.",
			},
			metricsLabels,
		)
		duration := prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: metricsNamespace,
				Subsystem: metricsSubsystem,
				Name:      "request_duration_seconds",
				Help:      "The HTTP request latencies in seconds.",
				Buckets:   prometheus.DefBuckets,
			},
			metricsLabels,
		)
		reg.MustRegister(requests, duration)

		metrics = &Metrics{registry: reg, requests: requests, duration: duration}
	})
	return metrics
}

// MetricsMiddleware counts requests and observes their duration, labelled by
// route, method and status. The route label is Echo's path template rather than
// the concrete URL, so every /boards/:id request collapses into one series; the
// method label is the routed verb set rather than the token the client sent. The
// cardinality of the whole metric is therefore the product of three sets this
// server owns, and no request can enlarge any of them.
func MetricsMiddleware() echo.MiddlewareFunc {
	m := appMetrics()
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := time.Now()

			// The observation is deferred so that it also happens while a panic
			// unwinds. echomw.Recover sits OUTSIDE this middleware — it has to,
			// or a panic in the middleware below it would escape the server — so
			// a panicking handler used to unwind straight past an inline
			// recording here, and the 500 the recovery then wrote landed in no
			// series at all: the one class of traffic most worth alerting on was
			// the one class invisible to the alert.
			//
			// returned separates the two ways this frame can end, and it has to
			// be tracked explicitly rather than inferred from what the response
			// carries. Echo resets Response.Status to 200 for every request, and
			// statusOf only reaches its 500 fallback for a non-nil error, so an
			// unwinding request — no error returned, nothing committed — would
			// otherwise be counted as a success.
			var (
				err      error
				returned bool
			)
			defer func() {
				status := http.StatusInternalServerError
				if returned {
					status = statusOf(c, err)
				}

				// Echo leaves the path empty when the router matched nothing, and
				// the request path may never stand in for it. The router runs
				// before the middleware chain, so this reads the same either way.
				route := c.Path()
				if route == "" {
					route = routeUnmatched
				}

				labels := prometheus.Labels{
					"code":   strconv.Itoa(status),
					"method": methodLabel(c.Request().Method),
					"route":  route,
				}
				m.requests.With(labels).Inc()
				m.duration.With(labels).Observe(time.Since(start).Seconds())
			}()

			err = next(c)
			returned = true
			return err
		}
	}
}

// statusOf reads the status the response will carry. The handler chain may have
// returned an error before anything was written, so the error decides the code
// whenever the response itself has not been committed to one.
func statusOf(c echo.Context, err error) int {
	status := c.Response().Status
	if err == nil {
		return status
	}
	var httpError *echo.HTTPError
	if errors.As(err, &httpError) {
		status = httpError.Code
	}
	if status == 0 || status == http.StatusOK {
		status = http.StatusInternalServerError
	}
	return status
}

// MetricsHandler renders the registry in the Prometheus text format.
//
// The endpoint carries no auth of its own: it is network-gated, not route-gated
// (SAAS_PLATFORM_PLAN.md §II.0.4). Whoever mounts it must keep it off the public
// reverse proxy and reachable only from the scrape network.
func MetricsHandler() echo.HandlerFunc {
	return echo.WrapHandler(promhttp.HandlerFor(appMetrics().registry, promhttp.HandlerOpts{}))
}
