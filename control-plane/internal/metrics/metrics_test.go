package metrics_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	cpmetrics "github.com/nunocgoncalves/iterabase-mono/control-plane/internal/metrics"
)

func TestMetricsIsolatedRegistryAndBoundedHTTPLabels(t *testing.T) {
	m := cpmetrics.New("api", "1.2.3", "abc123")
	router := chi.NewRouter()
	router.Use(m.HTTPMiddleware("api"))
	router.Get("/v1/work-items/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("created"))
	})

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/work-items/00000000-0000-0000-0000-000000000001", nil))
	require.Equal(t, http.StatusCreated, rr.Code)

	count := testutil.ToFloat64(m.HTTPRequests.WithLabelValues("api", "GET", "/v1/work-items/{id}", "2xx"))
	assert.Equal(t, float64(1), count)

	families, err := m.Registry.Gather()
	require.NoError(t, err)
	rendered := make([]string, 0, len(families))
	for _, family := range families {
		rendered = append(rendered, family.String())
	}
	all := strings.Join(rendered, "\n")
	assert.Contains(t, all, `name:"component"`)
	assert.Contains(t, all, `value:"api"`)
	assert.Contains(t, all, `value:"1.2.3"`)
	assert.NotContains(t, all, "00000000-0000-0000-0000-000000000001")
}

func TestProcedureMiddlewareUsesFixedProcedureAndMethodLabels(t *testing.T) {
	m := cpmetrics.New("gateway", "1.2.3", "abc123")
	const procedure = "/iterabase.gateway.v1.GatewayService/InvokeTool"
	handler := m.ProcedureMiddleware("gateway-rpc", procedure)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, procedure, nil))
	privateSuffix := "/iterabase.gateway.v1.GatewayService/customer-private-value"
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("CUSTOM-VERB", privateSuffix, nil))

	assert.Equal(t, float64(1), testutil.ToFloat64(
		m.HTTPRequests.WithLabelValues("gateway-rpc", http.MethodPost, procedure, "2xx"),
	))
	assert.Equal(t, float64(1), testutil.ToFloat64(
		m.HTTPRequests.WithLabelValues("gateway-rpc", "other", "unmatched", "2xx"),
	))

	families, err := m.Registry.Gather()
	require.NoError(t, err)
	var rendered []string
	for _, family := range families {
		rendered = append(rendered, family.String())
	}
	all := strings.Join(rendered, "\n")
	assert.NotContains(t, all, "CUSTOM-VERB")
	assert.NotContains(t, all, "customer-private-value")
}

func TestStatusClass(t *testing.T) {
	assert.Equal(t, "2xx", cpmetrics.StatusClass(http.StatusNoContent))
	assert.Equal(t, "unknown", cpmetrics.StatusClass(0))
}
