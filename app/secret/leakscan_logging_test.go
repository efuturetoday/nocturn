package secret_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/efuturetoday/nocturn/app/secret"
)

type capHandler struct {
	mu    *sync.Mutex
	recs  *[]capRec
	attrs []slog.Attr
}

type capRec struct {
	level slog.Level
	msg   string
	attrs map[string]string
}

func newCap() *capHandler { return &capHandler{mu: &sync.Mutex{}, recs: &[]capRec{}} }

func (h *capHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *capHandler) Handle(_ context.Context, r slog.Record) error {
	m := map[string]string{}
	for _, a := range h.attrs {
		m[a.Key] = a.Value.String()
	}
	r.Attrs(func(a slog.Attr) bool { m[a.Key] = a.Value.String(); return true })
	h.mu.Lock()
	*h.recs = append(*h.recs, capRec{r.Level, r.Message, m})
	h.mu.Unlock()
	return nil
}

func (h *capHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return &capHandler{mu: h.mu, recs: h.recs, attrs: append(append([]slog.Attr{}, h.attrs...), as...)}
}

func (h *capHandler) WithGroup(string) slog.Handler { return h }

// TestScanEgress_LogsBlockWithoutValue: a blocked egress is traced at Warn, and NO log field carries
// the secret value — only the tier.
func TestScanEgress_LogsBlockWithoutValue(t *testing.T) {
	const val = "SUPERSECRETVALUE123"
	store := secret.NewStore()
	store.Set("api", []byte(val))
	sc := secret.NewScanner(store)
	cap := newCap()
	sc.SetLogger(slog.New(cap))

	if err := sc.ScanEgress("token=" + val); !errors.Is(err, secret.ErrLeaked) {
		t.Fatalf("want ErrLeaked, got %v", err)
	}

	var found bool
	for _, r := range *cap.recs {
		if r.level == slog.LevelWarn && r.msg == "egress blocked" {
			found = true
			if r.attrs["tier"] != "vault" {
				t.Errorf("block log tier = %q, want vault", r.attrs["tier"])
			}
		}
		for k, v := range r.attrs {
			if strings.Contains(v, val) {
				t.Fatalf("LOG LEAKED the secret value in attr %q=%q", k, v)
			}
		}
	}
	if !found {
		t.Fatalf("no Warn 'egress blocked' logged; records=%+v", *cap.recs)
	}
}
