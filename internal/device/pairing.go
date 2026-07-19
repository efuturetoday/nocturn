package device

import (
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"sync"
	"time"
)

// pairTTL is how long a pending pairing (bootstrap or join) stays redeemable. Short, because
// it only has to survive a human scanning a QR or reading a code off a trusted screen.
const pairTTL = 3 * time.Minute

// maxAttempts is the redeem-failure budget for one pending pairing before it is burned. It
// bounds online brute-force of the 6-digit code/OTP to a negligible success probability
// (≤ maxAttempts / 1e6 per window).
const maxAttempts = 5

// maxOpenJoins caps the number of live join pendings. /join is UNAUTHENTICATED (a new device has
// no bearer yet), so without a bound a LAN attacker could spam it and exhaust memory. Every mint
// first sweeps expired pendings and then evicts the oldest join past this cap, so the map is
// bounded regardless of whether an admin is connected to trigger a lazy sweep.
const maxOpenJoins = 32

// Redeem outcomes, all fail-closed. A caller maps them to a 4xx; none reveals which field of a
// pending was wrong beyond bad-vs-expired-vs-exhausted.
var (
	ErrNoPending       = errors.New("device: no pending pairing")
	ErrExpired         = errors.New("device: pairing expired")
	ErrTooManyAttempts = errors.New("device: too many attempts")
	ErrBadCredential   = errors.New("device: bad credential")
)

// kind distinguishes the two pending flows so a redeem matches the right credential.
type kind int

const (
	kindBootstrap kind = iota // fresh boot: QR secret + stdout OTP, redeemed by /pair
	kindJoin                  // running daemon: a code shown to authed devices, redeemed by /join/confirm
)

// Pending is one short-lived, single-use pairing awaiting redemption. A bootstrap pending
// carries Secret (the QR's 32 bytes) and OTP (the typed fallback); a join pending carries Code
// (typed on the new device from a trusted screen) and the Name the new device gave. Expiry and
// Attempts enforce the short window and the brute-force budget.
type Pending struct {
	ID       string // the id; also the joinId a new device confirms with
	Secret   string // bootstrap: the QR credential
	OTP      string // bootstrap: the typed fallback
	Code     string // join: the code revealed to authed devices, typed on the new device
	Name     string // join: the new device's name (bootstrap takes the name at redeem time)
	Platform string // join: the joining device's OS ("ios" | "android"), routed to the push provider
	Expiry   time.Time
	kind     kind
	attempts int
}

// Pairings holds the in-memory pending pairings — at most a handful at once (one bootstrap
// slot, plus any in-flight joins). now is injectable so tests drive expiry without sleeping.
// Safe for concurrent use.
type Pairings struct {
	mu      sync.Mutex
	pending map[string]*Pending
	now     func() time.Time
}

// NewPairings builds an empty manager. Pass nil now to use the wall clock.
func NewPairings(now func() time.Time) *Pairings {
	if now == nil {
		now = time.Now
	}
	return &Pairings{pending: map[string]*Pending{}, now: now}
}

// MintBootstrap creates the fresh-boot pending: a 32-byte QR Secret plus a 6-digit typed OTP.
// It replaces any existing bootstrap pending (only one is ever live), so an old code cannot be
// redeemed once a new one is shown.
func (p *Pairings) MintBootstrap() Pending {
	pend := &Pending{
		ID:     hexToken(16),
		Secret: hexToken(32),
		OTP:    otpCode(),
		Expiry: p.now().Add(pairTTL),
		kind:   kindBootstrap,
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, e := range p.pending {
		if e.kind == kindBootstrap {
			delete(p.pending, id) // only one bootstrap live at a time
		}
	}
	p.pending[pend.ID] = pend
	return *pend
}

// MintJoin creates a join pending for a new device that gave its name and OS platform: a short
// Code the daemon reveals ONLY to already-authed devices (via OpenJoins), never to the requester.
// The returned Pending's ID is the joinId the new device confirms with.
func (p *Pairings) MintJoin(name, platform string) Pending {
	pend := &Pending{
		ID:       hexToken(16),
		Code:     otpCode(),
		Name:     name,
		Platform: platform,
		Expiry:   p.now().Add(pairTTL),
		kind:     kindJoin,
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sweepExpiredLocked() // the spam path cleans up after itself — no background GC needed
	p.capJoinsLocked()     // bound live joins even if no admin ever connects to trigger a sweep
	p.pending[pend.ID] = pend
	return *pend
}

// sweepExpiredLocked drops every timed-out pending. The caller holds p.mu.
func (p *Pairings) sweepExpiredLocked() {
	now := p.now()
	for id, e := range p.pending {
		if now.After(e.Expiry) {
			delete(p.pending, id)
		}
	}
}

// capJoinsLocked evicts the oldest join pending when the live count is at the cap, so a fresh
// (legitimate) join still gets in while runaway spam is bounded. The caller holds p.mu.
func (p *Pairings) capJoinsLocked() {
	var count int
	var oldest *Pending
	for _, e := range p.pending {
		if e.kind != kindJoin {
			continue
		}
		count++
		if oldest == nil || e.Expiry.Before(oldest.Expiry) {
			oldest = e
		}
	}
	if count >= maxOpenJoins && oldest != nil {
		delete(p.pending, oldest.ID)
	}
}

// RedeemBootstrap consumes the bootstrap pending matching credential (its Secret or OTP) and
// mints a device named name on the given platform, returning its bearer. It enforces expiry, the
// attempt budget, and single-use.
func (p *Pairings) RedeemBootstrap(credential, name, platform string, store *Store) (bearer string, err error) {
	if err := p.claimBootstrap(credential); err != nil {
		return "", err
	}
	_, bearer, err = store.Add(name, platform)
	return bearer, err
}

// RedeemJoin consumes the join pending identified by joinId if code matches, and mints a device
// with the name and platform the new device gave at MintJoin. Same expiry / attempt / single-use
// guarantees.
func (p *Pairings) RedeemJoin(joinID, code string, store *Store) (bearer string, err error) {
	name, platform, err := p.claimJoin(joinID, code)
	if err != nil {
		return "", err
	}
	_, bearer, err = store.Add(name, platform)
	return bearer, err
}

// OpenJoins returns the live join pendings for display on already-authed devices — the trusted
// surface a human reads the code from. Expired ones are pruned as a side effect.
func (p *Pairings) OpenJoins() []Pending {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	out := make([]Pending, 0, len(p.pending))
	for id, e := range p.pending {
		if e.kind != kindJoin {
			continue
		}
		if now.After(e.Expiry) {
			delete(p.pending, id)
			continue
		}
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Expiry.Before(out[j].Expiry) })
	return out
}

// claimBootstrap validates credential against the live bootstrap pending and, on success,
// removes it (single-use) so the caller can mint the device WITHOUT holding p.mu across the
// store's file I/O. A wrong credential burns one attempt; the budget or expiry removes it.
func (p *Pairings) claimBootstrap(credential string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	var pend *Pending
	for _, e := range p.pending {
		if e.kind == kindBootstrap {
			pend = e
			break
		}
	}
	if pend == nil {
		return ErrNoPending
	}
	return p.claimLocked(pend, constEq(credential, pend.Secret) || constEq(credential, pend.OTP))
}

// claimJoin validates code against the join pending identified by joinID and, on success,
// removes it and returns the name and platform it was minted with.
func (p *Pairings) claimJoin(joinID, code string) (name, platform string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pend := p.pending[joinID]
	if pend == nil || pend.kind != kindJoin {
		return "", "", ErrNoPending
	}
	name, platform = pend.Name, pend.Platform
	if err := p.claimLocked(pend, constEq(code, pend.Code)); err != nil {
		return "", "", err
	}
	return name, platform, nil
}

// constEq reports whether a and b are equal in constant time — the pending credentials (secret,
// OTP, join code) are compared with it so a match reveals nothing through timing.
func constEq(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// claimLocked applies the shared expiry / match / attempt-budget / single-use rules to a
// located pending. The caller holds p.mu; credential emptiness is treated as a mismatch by the
// callers (an empty string never equals a minted secret/otp/code). ok reports a credential match.
func (p *Pairings) claimLocked(pend *Pending, ok bool) error {
	if p.now().After(pend.Expiry) {
		delete(p.pending, pend.ID)
		return ErrExpired
	}
	if !ok {
		pend.attempts++
		if pend.attempts >= maxAttempts {
			delete(p.pending, pend.ID)
			return ErrTooManyAttempts
		}
		return ErrBadCredential
	}
	delete(p.pending, pend.ID) // single-use: claimed
	return nil
}

// otpCode returns a uniform 6-digit code, zero-padded, drawn from crypto/rand (not math/rand).
// crypto/rand.Int rejection-samples, so the distribution over [0,1e6) stays uniform.
func otpCode() string {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		panic("device: out of randomness: " + err.Error())
	}
	return fmt.Sprintf("%06d", n.Int64())
}
