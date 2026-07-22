package serve

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// broadcast never blocks on a congested connection: a conn whose buffer is full drops the message
// (and resyncs later), while others still receive it.
func TestHub_Broadcast_NonBlocking_DropsFullBuffer(t *testing.T) {
	h := newHub()

	full := &conn{out: make(chan any, 64)}
	for range 64 { // saturate the buffer (newConn's cap)
		full.out <- "filler"
	}
	open := &conn{out: make(chan any, 64)}

	h.add(full)
	h.add(open)

	done := make(chan struct{})
	go func() {
		h.broadcast("live")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("broadcast blocked on a full connection")
	}

	if len(full.out) != 64 {
		t.Errorf("full conn buffer = %d, want 64 (message dropped, not enqueued)", len(full.out))
	}
	select {
	case got := <-open.out:
		if got != "live" {
			t.Errorf("open conn received %v, want live", got)
		}
	default:
		t.Error("open conn did not receive the broadcast")
	}
}

// Concurrent broadcasts with a live reader must be race-clean (run with -race).
func TestHub_Broadcast_Concurrent(t *testing.T) {
	h := newHub()
	c := &conn{out: make(chan any, 64)}
	h.add(c)

	// Drain continuously so the buffer never wedges.
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-c.out:
			}
		}
	}()

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				h.broadcast("msg")
			}
		}()
	}
	// Churn membership concurrently to stress the hub's lock.
	wg.Add(1)
	go func() {
		defer wg.Done()
		other := &conn{out: make(chan any, 64)}
		for range 100 {
			h.add(other)
			h.remove(other)
		}
	}()
	wg.Wait()
	close(stop)
}

func TestCors_OptionsPreflight204(t *testing.T) {
	var innerCalled bool
	h := cors(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { innerCalled = true }))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/pair", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("preflight status = %d, want 204", rec.Code)
	}
	if innerCalled {
		t.Error("preflight must not reach the wrapped handler")
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Allow-Origin = %q, want *", got)
	}
}

func TestCors_PassesThroughNonPreflight(t *testing.T) {
	var innerCalled bool
	h := cors(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		innerCalled = true
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/join", nil))

	if !innerCalled {
		t.Error("non-preflight request must reach the wrapped handler")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Allow-Origin = %q, want *", got)
	}
}
