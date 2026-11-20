package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMiddlewareAuthSkipsOnvifPath(t *testing.T) {
	h := middlewareAuth("admin", "pass", true, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "http://example.com/onvif/device_service", nil)
	req.RemoteAddr = "203.0.113.10:12345"
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
}

func TestMiddlewareAuthProtectsAPIPath(t *testing.T) {
	h := middlewareAuth("admin", "pass", true, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "http://example.com/api", nil)
	req.RemoteAddr = "203.0.113.10:12345"
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestMiddlewareAuthSkipsHomeKitPairing(t *testing.T) {
	h := middlewareAuth("admin", "pass", true, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for _, path := range []string{"/pair-setup", "/pair-verify"} {
		req := httptest.NewRequest(http.MethodPost, "http://example.com"+path, nil)
		req.RemoteAddr = "203.0.113.10:12345"
		rr := httptest.NewRecorder()

		h.ServeHTTP(rr, req)
		require.Equal(t, http.StatusOK, rr.Code, path)
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/api/homekit", nil)
	req.RemoteAddr = "203.0.113.10:12345"
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusUnauthorized, rr.Code)
}
