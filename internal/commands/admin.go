package commands

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/stellar/go-stellar-sdk/support/log"
)

// adminMaxBodyBytes bounds one admin request body. The largest legal
// payload is a MaxReseedBatch reseed (~300KB of contract ids); 1MB
// leaves headroom without accepting arbitrary uploads.
const adminMaxBodyBytes = 1 << 20

// RegistryReader is the read-only slice of the escrow registry the admin
// surface exposes. *registry.Registry satisfies it; reads are safe
// concurrently with the loop's writes (the registry is RWMutex-guarded).
type RegistryReader interface {
	Snapshot() []string
	RemovedSnapshot() []string
	Size() int
}

// AdminHandler serves the authenticated control surface, mounted on the
// health server (never a public port — same rule as graph-node's admin
// JSON-RPC). Every write endpoint only VALIDATES and ENQUEUES a command;
// the ingest loop executes between ledgers, staying the single writer.
// 202 Accepted therefore means "queued", never "done" — the audit log
// carries the execution outcome.
//
// This is the ONLY way a command enters the pipeline. That is the whole
// point of the A1 audit fix: while an AMQP consumer also fed this
// channel, every command — including pause and remove_escrow — was
// reachable by anyone who could publish to the broker, with no
// authorization anywhere in the path.
//
// Auth: Authorization: Bearer <token>, compared in constant time. An
// empty configured token disables the whole surface (the caller should
// not even mount it; the in-handler guard is defence in depth).
func AdminHandler(token string, enqueue func(Command) error, reg RegistryReader) http.Handler {
	mux := http.NewServeMux()

	accept := func(w http.ResponseWriter, r *http.Request, cmd Command) {
		if err := cmd.Validate(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := enqueue(cmd); err != nil {
			http.Error(w, "command queue is full, retry shortly", http.StatusServiceUnavailable)
			return
		}
		log.Ctx(r.Context()).Infof("Admin accepted command %s (requested_by=%q)", cmd, cmd.RequestedBy)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{"accepted": true, "command": cmd.String()})
	}

	readBody := func(w http.ResponseWriter, r *http.Request, into *Command) bool {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, adminMaxBodyBytes))
		if err != nil {
			http.Error(w, "reading body: "+err.Error(), http.StatusBadRequest)
			return false
		}
		if len(body) == 0 {
			return true // command fully determined by the route
		}
		if err := json.Unmarshal(body, into); err != nil {
			http.Error(w, "malformed JSON body", http.StatusBadRequest)
			return false
		}
		return true
	}

	// POST /admin/escrows {"contract_id": "C..."} — start tracking.
	mux.HandleFunc("POST /admin/escrows", func(w http.ResponseWriter, r *http.Request) {
		var cmd Command
		if !readBody(w, r, &cmd) {
			return
		}
		cmd.Kind = KindTrackEscrow
		accept(w, r, cmd)
	})

	// POST /admin/escrows/{id}/refresh — re-publish current state.
	mux.HandleFunc("POST /admin/escrows/{id}/refresh", func(w http.ResponseWriter, r *http.Request) {
		accept(w, r, Command{Kind: KindRefreshEscrow, ContractID: r.PathValue("id")})
	})

	// DELETE /admin/escrows/{id} — tombstone.
	mux.HandleFunc("DELETE /admin/escrows/{id}", func(w http.ResponseWriter, r *http.Request) {
		accept(w, r, Command{Kind: KindRemoveEscrow, ContractID: r.PathValue("id")})
	})

	// POST /admin/reseed {"contract_ids": ["C...", ...]} — bulk recovery.
	mux.HandleFunc("POST /admin/reseed", func(w http.ResponseWriter, r *http.Request) {
		var cmd Command
		if !readBody(w, r, &cmd) {
			return
		}
		cmd.Kind = KindReseed
		accept(w, r, cmd)
	})

	// POST /admin/pause {"ttl_seconds": 600} — bounded halt.
	mux.HandleFunc("POST /admin/pause", func(w http.ResponseWriter, r *http.Request) {
		var cmd Command
		if !readBody(w, r, &cmd) {
			return
		}
		cmd.Kind = KindPause
		accept(w, r, cmd)
	})

	mux.HandleFunc("POST /admin/resume", func(w http.ResponseWriter, r *http.Request) {
		accept(w, r, Command{Kind: KindResume})
	})

	mux.HandleFunc("POST /admin/reconcile", func(w http.ResponseWriter, r *http.Request) {
		accept(w, r, Command{Kind: KindReconcile})
	})

	// GET /admin/registry — the tracked and tombstoned sets.
	mux.HandleFunc("GET /admin/registry", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{
			"count":   reg.Size(),
			"escrows": reg.Snapshot(),
			"removed": reg.RemovedSnapshot(),
		})
	})

	return requireBearer(token, mux)
}

// EnqueueInto returns an enqueue func feeding ch without ever blocking
// an HTTP handler: a full queue is reported, not waited out.
func EnqueueInto(ch chan<- Command) func(Command) error {
	return func(cmd Command) error {
		select {
		case ch <- cmd:
			return nil
		default:
			return errors.New("command queue full")
		}
	}
}

// requireBearer wraps next with constant-time bearer-token auth.
func requireBearer(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token == "" {
			http.Error(w, "admin surface disabled: ADMIN_TOKEN is not set", http.StatusForbidden)
			return
		}
		const prefix = "Bearer "
		auth := r.Header.Get("Authorization")
		if len(auth) <= len(prefix) || auth[:len(prefix)] != prefix ||
			subtle.ConstantTimeCompare([]byte(auth[len(prefix):]), []byte(token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
