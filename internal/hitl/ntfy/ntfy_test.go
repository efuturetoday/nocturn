package ntfy_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/efuturetoday/nocturn/internal/hitl"
	"github.com/efuturetoday/nocturn/internal/hitl/ntfy"
)

func TestPublisher_Notify_PublishesRequestWithActionTokens(t *testing.T) {
	var gotBody []byte
	var gotContentType, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotContentType = r.Header.Get("Content-Type")
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	respURL := srv.URL + "/nocturn-responses"
	p := ntfy.New(srv.URL, "nocturn-requests", respURL, ntfy.WithAuth("secret-tok"))

	opts := []hitl.Option{
		{Label: "Allow once", Outcome: hitl.Approved, Token: "APPROVE_TOKEN"},
		{Label: "Deny", Outcome: hitl.Denied, Token: "DENY_TOKEN"},
	}
	if err := p.Notify("send email to boss", opts); err != nil {
		t.Fatalf("notify: %v", err)
	}

	if gotContentType != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", gotContentType)
	}
	if gotAuth != "Bearer secret-tok" {
		t.Fatalf("Authorization = %q, want Bearer secret-tok", gotAuth)
	}

	var msg struct {
		Topic   string `json:"topic"`
		Message string `json:"message"`
		Actions []struct {
			Action, Label, URL, Method, Body string
		} `json:"actions"`
	}
	if err := json.Unmarshal(gotBody, &msg); err != nil {
		t.Fatalf("published body is not valid JSON: %v (%s)", err, gotBody)
	}

	if msg.Topic != "nocturn-requests" {
		t.Fatalf("topic = %q", msg.Topic)
	}
	if msg.Message != "send email to boss" {
		t.Fatalf("message = %q", msg.Message)
	}
	if len(msg.Actions) != 2 {
		t.Fatalf("want 2 actions, got %d", len(msg.Actions))
	}
	approve, deny := msg.Actions[0], msg.Actions[1]
	if approve.Label != "Allow once" || approve.Body != "APPROVE_TOKEN" || approve.URL != respURL || approve.Method != "POST" {
		t.Fatalf("approve action wrong: %+v", approve)
	}
	if deny.Label != "Deny" || deny.Body != "DENY_TOKEN" {
		t.Fatalf("deny action wrong: %+v", deny)
	}
}

func TestPublisher_Notify_NonSuccessIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := ntfy.New(srv.URL, "topic", srv.URL+"/resp")
	opts := []hitl.Option{{Label: "Allow", Outcome: hitl.Approved, Token: "a"}}
	if err := p.Notify("intent", opts); err == nil {
		t.Fatal("a non-2xx publish must return an error")
	}
}
