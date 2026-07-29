package commands

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeRegistry struct {
	escrows []string
	removed []string
}

func (f *fakeRegistry) Snapshot() []string        { return f.escrows }
func (f *fakeRegistry) RemovedSnapshot() []string { return f.removed }
func (f *fakeRegistry) Size() int                 { return len(f.escrows) }

func adminReq(t *testing.T, h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestAdmin_RequiresBearerToken(t *testing.T) {
	var got []Command
	h := AdminHandler("s3cret", func(c Command) error { got = append(got, c); return nil }, &fakeRegistry{})

	if rr := adminReq(t, h, "GET", "/admin/registry", "", ""); rr.Code != http.StatusUnauthorized {
		t.Errorf("no token: status = %d, want 401", rr.Code)
	}
	if rr := adminReq(t, h, "GET", "/admin/registry", "wrong", ""); rr.Code != http.StatusUnauthorized {
		t.Errorf("wrong token: status = %d, want 401", rr.Code)
	}
	if rr := adminReq(t, h, "POST", "/admin/reconcile", "wrong", ""); rr.Code != http.StatusUnauthorized {
		t.Errorf("write with wrong token: status = %d, want 401", rr.Code)
	}
	if len(got) != 0 {
		t.Fatalf("unauthorized requests must never enqueue; got %v", got)
	}
	if rr := adminReq(t, h, "GET", "/admin/registry", "s3cret", ""); rr.Code != http.StatusOK {
		t.Errorf("valid token: status = %d, want 200", rr.Code)
	}
}

func TestAdmin_EmptyTokenDisablesSurface(t *testing.T) {
	h := AdminHandler("", func(Command) error { return nil }, &fakeRegistry{})
	if rr := adminReq(t, h, "GET", "/admin/registry", "", ""); rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 when ADMIN_TOKEN is unset", rr.Code)
	}
}

func TestAdmin_RoutesEnqueueTheRightCommands(t *testing.T) {
	var got []Command
	h := AdminHandler("tok", func(c Command) error { got = append(got, c); return nil }, &fakeRegistry{})

	tests := []struct {
		method, path, body string
		want               Kind
		contract           string
	}{
		{"POST", "/admin/escrows", `{"contract_id":"CABC"}`, KindTrackEscrow, "CABC"},
		{"POST", "/admin/escrows/CABC/refresh", "", KindRefreshEscrow, "CABC"},
		{"DELETE", "/admin/escrows/CABC", "", KindRemoveEscrow, "CABC"},
		{"POST", "/admin/reseed", `{"contract_ids":["C1","C2"]}`, KindReseed, ""},
		{"POST", "/admin/pause", `{"ttl_seconds":120}`, KindPause, ""},
		{"POST", "/admin/resume", "", KindResume, ""},
		{"POST", "/admin/reconcile", "", KindReconcile, ""},
	}
	for _, tt := range tests {
		got = nil
		rr := adminReq(t, h, tt.method, tt.path, "tok", tt.body)
		if rr.Code != http.StatusAccepted {
			t.Errorf("%s %s: status = %d, want 202 (%s)", tt.method, tt.path, rr.Code, rr.Body.String())
			continue
		}
		if len(got) != 1 || got[0].Kind != tt.want {
			t.Errorf("%s %s: enqueued %v, want kind %s", tt.method, tt.path, got, tt.want)
			continue
		}
		if tt.contract != "" && got[0].ContractID != tt.contract {
			t.Errorf("%s %s: contract = %q, want %q", tt.method, tt.path, got[0].ContractID, tt.contract)
		}
	}
}

func TestAdmin_InvalidBodyIsRejectedBeforeEnqueue(t *testing.T) {
	var got []Command
	h := AdminHandler("tok", func(c Command) error { got = append(got, c); return nil }, &fakeRegistry{})

	if rr := adminReq(t, h, "POST", "/admin/escrows", "tok", `{"contract_id":""}`); rr.Code != http.StatusBadRequest {
		t.Errorf("track without id: status = %d, want 400", rr.Code)
	}
	if rr := adminReq(t, h, "POST", "/admin/pause", "tok", `{"ttl_seconds":999999}`); rr.Code != http.StatusBadRequest {
		t.Errorf("pause over max ttl: status = %d, want 400", rr.Code)
	}
	// Body decoding is the admin surface's job since the AMQP consumer
	// (which used to own it) was removed — so a truncated body must be
	// rejected here, not panic or enqueue a zero-valued command.
	if rr := adminReq(t, h, "POST", "/admin/escrows", "tok", `{"contract_id":`); rr.Code != http.StatusBadRequest {
		t.Errorf("malformed JSON: status = %d, want 400", rr.Code)
	}
	if len(got) != 0 {
		t.Fatalf("invalid commands must never enqueue; got %v", got)
	}
}

func TestAdmin_FullQueueReports503(t *testing.T) {
	h := AdminHandler("tok", func(Command) error { return errors.New("full") }, &fakeRegistry{})
	if rr := adminReq(t, h, "POST", "/admin/reconcile", "tok", ""); rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 when the queue is full", rr.Code)
	}
}

func TestAdmin_RegistryListsTrackedAndTombstoned(t *testing.T) {
	reg := &fakeRegistry{escrows: []string{"C1", "C2"}, removed: []string{"C9"}}
	h := AdminHandler("tok", func(Command) error { return nil }, reg)

	rr := adminReq(t, h, "GET", "/admin/registry", "tok", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var body struct {
		Count   int      `json:"count"`
		Escrows []string `json:"escrows"`
		Removed []string `json:"removed"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body.Count != 2 || len(body.Escrows) != 2 || len(body.Removed) != 1 {
		t.Errorf("body = %+v, want 2 escrows and 1 removed", body)
	}
}

func TestEnqueueInto_NeverBlocks(t *testing.T) {
	ch := make(chan Command, 1)
	enqueue := EnqueueInto(ch)
	if err := enqueue(Command{Kind: KindResume}); err != nil {
		t.Fatalf("first enqueue: %v", err)
	}
	if err := enqueue(Command{Kind: KindResume}); err == nil {
		t.Fatal("expected an error on a full queue, not a block")
	}
}
