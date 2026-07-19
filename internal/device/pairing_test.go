package device_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/internal/device"
)

// clock is a controllable time source for driving pending-pairing expiry without sleeping.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func TestPairings_BootstrapRedeem(t *testing.T) {
	for _, tc := range []struct {
		name string
		cred func(p device.Pending) string
	}{
		{"secret", func(p device.Pending) string { return p.Secret }},
		{"otp", func(p device.Pending) string { return p.OTP }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := device.Load("")
			p := device.NewPairings((&clock{t: time.Unix(1000, 0)}).now)
			pend := p.MintBootstrap()

			bearer, err := p.RedeemBootstrap(tc.cred(pend), "phone", "ios", store)
			if err != nil {
				t.Fatalf("RedeemBootstrap: %v", err)
			}
			if d, ok := store.Verify(bearer); !ok || d.Name != "phone" {
				t.Fatalf("minted bearer not in store (ok=%v device=%+v)", ok, d)
			}
			// Single-use: a second redeem of the same code finds nothing.
			if _, err := p.RedeemBootstrap(tc.cred(pend), "phone", "ios", store); !errors.Is(err, device.ErrNoPending) {
				t.Fatalf("second redeem err = %v, want ErrNoPending", err)
			}
		})
	}
}

func TestPairings_JoinRedeem(t *testing.T) {
	store := device.Load("")
	p := device.NewPairings((&clock{t: time.Unix(1000, 0)}).now)

	pend := p.MintJoin("iPad", "ios")
	if pend.ID == "" || pend.Code == "" {
		t.Fatalf("MintJoin returned empty id/code: %+v", pend)
	}

	// The code is revealed only via OpenJoins (the authed-device surface), keyed by the joinId.
	opens := p.OpenJoins()
	if len(opens) != 1 || opens[0].ID != pend.ID || opens[0].Code != pend.Code || opens[0].Name != "iPad" {
		t.Fatalf("OpenJoins = %+v, want the one join with its code", opens)
	}

	// A wrong code does not redeem.
	if _, err := p.RedeemJoin(pend.ID, "000000", store); !errors.Is(err, device.ErrBadCredential) {
		t.Fatalf("wrong-code redeem err = %v, want ErrBadCredential", err)
	}
	// The right code mints a device named as the joiner asked.
	bearer, err := p.RedeemJoin(pend.ID, pend.Code, store)
	if err != nil {
		t.Fatalf("RedeemJoin: %v", err)
	}
	if d, ok := store.Verify(bearer); !ok || d.Name != "iPad" {
		t.Fatalf("minted join bearer not in store (ok=%v device=%+v)", ok, d)
	}
	if got := len(p.OpenJoins()); got != 0 {
		t.Fatalf("OpenJoins after redeem = %d, want 0 (single-use)", got)
	}
}

// /join is unauthenticated, so a flood must not grow the pending map without bound: each mint
// sweeps expired pendings and caps the live joins.
func TestPairings_JoinSpamBounded(t *testing.T) {
	const cap = 32 // mirrors maxOpenJoins
	clk := &clock{t: time.Unix(1000, 0)}
	p := device.NewPairings(clk.now)

	for range 500 {
		p.MintJoin("spam", "ios")
	}
	if n := len(p.OpenJoins()); n > cap {
		t.Fatalf("open joins after a flood = %d, want <= %d", n, cap)
	}

	// Expired joins are swept by the next mint even with no admin listing them.
	clk.advance(4 * time.Minute) // every live join is now past the TTL
	p.MintJoin("fresh", "ios")
	if n := len(p.OpenJoins()); n != 1 {
		t.Fatalf("open joins after TTL + one fresh mint = %d, want 1 (rest swept)", n)
	}
}

func TestPairings_Expiry(t *testing.T) {
	store := device.Load("")
	clk := &clock{t: time.Unix(1000, 0)}
	p := device.NewPairings(clk.now)
	pend := p.MintBootstrap()

	clk.advance(4 * time.Minute) // past the 3-minute TTL
	if _, err := p.RedeemBootstrap(pend.Secret, "phone", "ios", store); !errors.Is(err, device.ErrExpired) {
		t.Fatalf("expired redeem err = %v, want ErrExpired", err)
	}
	// Expired pending is gone.
	if _, err := p.RedeemBootstrap(pend.Secret, "phone", "ios", store); !errors.Is(err, device.ErrNoPending) {
		t.Fatalf("post-expiry redeem err = %v, want ErrNoPending", err)
	}
}

func TestPairings_AttemptBudget(t *testing.T) {
	store := device.Load("")
	p := device.NewPairings((&clock{t: time.Unix(1000, 0)}).now)
	pend := p.MintJoin("iPad", "ios")

	// Four wrong codes are rejected but keep the pending alive; the fifth burns it.
	for i := range maxTestAttempts - 1 {
		if _, err := p.RedeemJoin(pend.ID, "000000", store); !errors.Is(err, device.ErrBadCredential) {
			t.Fatalf("attempt %d err = %v, want ErrBadCredential", i, err)
		}
	}
	if _, err := p.RedeemJoin(pend.ID, "000000", store); !errors.Is(err, device.ErrTooManyAttempts) {
		t.Fatalf("final attempt err = %v, want ErrTooManyAttempts", err)
	}
	// Burned: even the correct code no longer works.
	if _, err := p.RedeemJoin(pend.ID, pend.Code, store); !errors.Is(err, device.ErrNoPending) {
		t.Fatalf("post-burn redeem err = %v, want ErrNoPending", err)
	}
}

// maxTestAttempts mirrors the package's maxAttempts budget (5). Kept local so the test states
// the expected count explicitly rather than importing an unexported constant.
const maxTestAttempts = 5
