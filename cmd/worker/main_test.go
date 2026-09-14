package main

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestHealthHandlerSeparatesLivenessAndReadiness(t *testing.T) {
	var ready atomic.Bool
	handler := healthHandler(&ready)

	assertStatus := func(path string, want int) {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != want {
			t.Fatalf("GET %s returned %d, want %d", path, response.Code, want)
		}
	}

	assertStatus("/health", http.StatusOK)
	assertStatus("/ready", http.StatusServiceUnavailable)
	ready.Store(true)
	assertStatus("/ready", http.StatusNoContent)
	assertStatus("/missing", http.StatusNotFound)
}
