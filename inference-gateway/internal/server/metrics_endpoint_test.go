package server

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"

	"github.com/nunocgoncalves/iterabase-mono/inference-gateway/internal/metrics"
)

func TestMetricsNotExposedOnIngressFacingRouter(t *testing.T) {
	router := newRouter(slog.Default(), metrics.New(prometheus.NewRegistry()), nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	assert.Equal(t, http.StatusNotFound, rr.Code)
}
