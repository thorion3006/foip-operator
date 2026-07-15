//go:build e2e

// cmd/fake-provider is a deterministic Netcup SCP-compatible test provider.
// It is built only for the Kind E2E image and must never be used in production.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type provider struct {
	mu          sync.Mutex
	owner       int
	nextTask    int
	routeCount  int
	tasks       map[string]time.Time
	routeDelay  time.Duration
	routeErrors int
}

func main() {
	p := &provider{owner: envInt("FAKE_PROVIDER_INITIAL_OWNER", 0), tasks: make(map[string]time.Time)}
	p.routeDelay = time.Duration(envInt("FAKE_PROVIDER_ROUTE_DELAY_SECONDS", 0)) * time.Second
	p.routeErrors = envInt("FAKE_PROVIDER_ROUTE_ERRORS", 0)
	mux := http.NewServeMux()
	mux.HandleFunc("/token", p.token)
	mux.HandleFunc("/state", p.state)
	mux.HandleFunc("/api/v1/users/", p.api)
	mux.HandleFunc("/api/v1/tasks/", p.task)
	address := ":" + envString("PORT", "8080")
	log.Printf("fake provider listening on %s", address)
	if err := http.ListenAndServe(address, mux); err != nil { // #nosec G114 -- test-only server
		log.Fatal(err)
	}
}

func (p *provider) token(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"access_token": "fake-token", "expires_in": 3600})
}

func (p *provider) state(w http.ResponseWriter, _ *http.Request) {
	p.mu.Lock()
	state := map[string]int{"owner": p.owner, "routeCount": p.routeCount}
	p.mu.Unlock()
	writeJSON(w, http.StatusOK, state)
}

func (p *provider) api(w http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodGet {
		p.mu.Lock()
		owner := p.owner
		p.mu.Unlock()
		response := []map[string]any{{"id": 17}}
		if owner != 0 {
			response[0]["server"] = map[string]int{"id": owner}
		}
		writeJSON(w, http.StatusOK, response)
		return
	}
	if request.Method != http.MethodPatch {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ServerID int `json:"serverId"`
	}
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body.ServerID == 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.routeErrors > 0 {
		p.routeErrors--
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	p.nextTask++
	p.routeCount++
	taskID := fmt.Sprintf("task-%d", p.nextTask)
	p.tasks[taskID] = time.Now().Add(p.routeDelay)
	p.owner = body.ServerID
	writeJSON(w, http.StatusAccepted, map[string]string{"uuid": taskID})
}

func (p *provider) task(w http.ResponseWriter, request *http.Request) {
	id := strings.TrimPrefix(request.URL.Path, "/api/v1/tasks/")
	p.mu.Lock()
	doneAt, found := p.tasks[id]
	p.mu.Unlock()
	if !found {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	state := "RUNNING"
	if !time.Now().Before(doneAt) {
		state = "FINISHED"
	}
	writeJSON(w, http.StatusOK, map[string]string{"state": state})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func envInt(name string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(name))
	if err != nil {
		return fallback
	}
	return value
}

func envString(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
