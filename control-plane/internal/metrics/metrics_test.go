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

func TestStatusClass(t *testing.T) {
	assert.Equal(t, "2xx", cpmetrics.StatusClass(http.StatusNoContent))
	assert.Equal(t, "unknown", cpmetrics.StatusClass(0))
}
