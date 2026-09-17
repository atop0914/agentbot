package app

import (
	"encoding/json"
	"net/http"
)

// NewRouter creates the HTTP handler with all routes wired.
func NewRouter(a *App) http.Handler {
	mux := http.NewServeMux()

	// Health check (unauthenticated)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	// Auth routes (public)
	a.AuthHandler.RegisterRoutes(mux)

	// Agent routes (authenticated)
	a.AgentH.RegisterRoutes(mux)

	// Cloud environment routes (authenticated)
	a.CloudH.RegisterRoutes(mux)

	// Task routes (authenticated)
	a.TaskMgr.RegisterRoutes(mux)

	// Communication routes (authenticated)
	a.CommH.RegisterRoutes(mux, "/api/v1")

	// WebSocket routes
	a.WSHandler.RegisterRoutes(mux, "/api/v1")

	// Browser automation routes
	a.BrowserH.RegisterRoutes(mux)

	// Terminal execution routes
	a.TerminalH.RegisterRoutes(mux)

	// Filesystem routes
	a.FileSystemH.RegisterRoutes(mux)

	// Apply global middleware chain: Recovery → RequestID → CORS → Logging
	var handler http.Handler = mux
	handler = loggingMiddleware(a.Logger)(handler)
	handler = corsMiddleware()(handler)
	handler = requestIDMiddleware()(handler)
	handler = recoveryMiddleware(a.Logger)(handler)

	return handler
}

// jsonOK writes a JSON 200 response.
func jsonOK(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(data)
}

// jsonError writes a JSON error response.
func jsonError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}
