package voice_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/gate"
	"github.com/efuturetoday/nocturn/internal/voice"
)

// --- fakes -----------------------------------------------------------------

// fakeLive hands out one scripted session. The test pushes events onto it and reads back what the
// driver sent, so a whole conversation runs with no provider and no network.
type fakeLive struct {
	sess *fakeSession
	conv []agentkit.Message
	spec []agentkit.ToolSpec
}

func (f *fakeLive) Open(_ context.Context, conv []agentkit.Message, tools []agentkit.ToolSpec) (agentkit.LiveSession, error) {
	f.conv, f.spec = conv, tools
	return f.sess, nil
}

type fakeSession struct {
	events  chan agentkit.LiveEvent
	audio   chan []byte
	results chan agentkit.ToolResult
	once    sync.Once
}

func newSession() *fakeSession {
	return &fakeSession{
		events:  make(chan agentkit.LiveEvent, 16),
		audio:   make(chan []byte, 16),
		results: make(chan agentkit.ToolResult, 16),
	}
}

func (s *fakeSession) SendAudio(_ context.Context, pcm []byte) error { s.audio <- pcm; return nil }
func (s *fakeSession) SendResult(_ context.Context, r agentkit.ToolResult) error {
	s.results <- r
	return nil
}
func (s *fakeSession) Events() <-chan agentkit.LiveEvent { return s.events }
func (s *fakeSession) Close() error                      { s.once.Do(func() { close(s.events) }); return nil }
func (s *fakeSession) push(ev agentkit.LiveEvent)        { s.events <- ev }

// fakeDevice is a satellite that never speaks unless the test tells it to.
type fakeDevice struct {
	mic        chan []byte
	played     chan []byte
	interrupts chan struct{}
}

func newDevice() *fakeDevice {
	return &fakeDevice{
		mic:        make(chan []byte),
		played:     make(chan []byte, 16),
		interrupts: make(chan struct{}, 4),
	}
}

func (d *fakeDevice) Recv(ctx context.Context) ([]byte, error) {
	select {
	case pcm, ok := <-d.mic:
		if !ok {
			return nil, io.EOF
		}
		return pcm, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (d *fakeDevice) Play(pcm []byte) error { d.played <- pcm; return nil }
func (d *fakeDevice) Interrupt() error      { d.interrupts <- struct{}{}; return nil }

// fakeObserver reports what the session committed, which is also how a test observes that the
// event loop has processed a turn — no sleeps.
type fakeObserver struct{ said chan agentkit.Message }

func newObserver() *fakeObserver { return &fakeObserver{said: make(chan agentkit.Message, 16)} }

func (o *fakeObserver) Said(role agentkit.Role, text string) {
	o.said <- agentkit.Message{Role: role, Content: text}
}
func (o *fakeObserver) ToolRan(string, string, string, error) {}

// tool builds a named tool whose Call runs fn.
func tool(name string, fn func(ctx context.Context, args string) (string, error)) agentkit.Tool {
	t, err := agentkit.NewTool(name, "test tool", fn)
	if err != nil {
		panic(err)
	}
	return t
}

func toolset(t *testing.T, tools ...agentkit.Tool) agentkit.ToolSet {
	t.Helper()
	ts, err := agentkit.NewToolSet(tools...)
	if err != nil {
		t.Fatalf("NewToolSet: %v", err)
	}
	return ts
}

func allow() gate.Policy {
	return gate.PolicyFunc(func(gate.Action) gate.Ruling { return gate.Allowed() })
}

// run starts a driver in the background and returns a func that waits for its transcript.
func run(t *testing.T, d *voice.Driver, dev voice.Device, conv []agentkit.Message) func() []agentkit.Message {
	t.Helper()
	var (
		msgs []agentkit.Message
		err  error
		done = make(chan struct{})
	)
	go func() {
		defer close(done)
		msgs, err = d.Run(t.Context(), dev, conv)
	}()
	return func() []agentkit.Message {
		t.Helper()
		<-done
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Run: %v", err)
		}
		return msgs
	}
}

// --- tests -----------------------------------------------------------------

// The whole point of the driver: tools reach the SAME ToolSet with no turn loop involved.
func TestToolCall_RunsTheToolAndAnswersTheModel(t *testing.T) {
	sess, dev := newSession(), newDevice()
	ts := toolset(t, tool("time_now", func(context.Context, string) (string, error) { return "12:00", nil }))
	d := voice.New(&fakeLive{sess: sess}, ts, allow(), gate.NewMemGrants(), nil)
	wait := run(t, d, dev, nil)

	sess.push(agentkit.LiveToolCall{ID: "c1", Tool: "time_now", Args: "{}"})
	got := <-sess.results
	if got.ID != "c1" || got.Result != "12:00" || got.Err != nil {
		t.Errorf("result = %+v, want c1/12:00/nil", got)
	}
	sess.Close()
	wait()
}

// A denial must come back as a RESULT. Swallowing it leaves the model waiting on a call that never
// gets answered, and the speaker goes silent mid-conversation.
func TestGateDenial_ReachesTheModelAsAResult(t *testing.T) {
	sess, dev := newSession(), newDevice()
	ran := false
	ts := toolset(t, gate.Wrap(tool("http_read", func(context.Context, string) (string, error) {
		ran = true
		return "leaked", nil
	})))
	deny := gate.PolicyFunc(func(gate.Action) gate.Ruling { return gate.Denied() })
	d := voice.New(&fakeLive{sess: sess}, ts, deny, gate.NewMemGrants(), nil)
	wait := run(t, d, dev, nil)

	sess.push(agentkit.LiveToolCall{ID: "c1", Tool: "http_read", Args: "{}"})
	got := <-sess.results
	if got.Err == nil || !errors.Is(got.Err, gate.ErrDenied) {
		t.Errorf("err = %v, want a gate denial", got.Err)
	}
	if ran {
		t.Error("the tool ran despite the policy denying it")
	}
	sess.Close()
	wait()
}

// With no approver an Ask fails closed. That is the posture a screenless satellite runs in, so it
// is worth pinning: a missing approver must never mean "allow".
func TestAskWithoutApprover_FailsClosed(t *testing.T) {
	sess, dev := newSession(), newDevice()
	ts := toolset(t, gate.Wrap(tool("file_read", func(context.Context, string) (string, error) { return "secret", nil })))
	ask := gate.PolicyFunc(func(gate.Action) gate.Ruling { return gate.AskWith(gate.RecallSession) })
	d := voice.New(&fakeLive{sess: sess}, ts, ask, gate.NewMemGrants(), nil) // nil approver
	wait := run(t, d, dev, nil)

	sess.push(agentkit.LiveToolCall{ID: "c1", Tool: "file_read", Args: "{}"})
	if got := <-sess.results; !errors.Is(got.Err, gate.ErrDeniedUnattended) {
		t.Errorf("err = %v, want ErrDeniedUnattended", got.Err)
	}
	sess.Close()
	wait()
}

// The load-bearing concurrency claim: a tool blocked on a human approval must not stall audio. If
// this regresses, every gated call freezes the conversation for as long as the human takes.
func TestPendingToolCall_DoesNotStallAudio(t *testing.T) {
	sess, dev := newSession(), newDevice()
	release := make(chan struct{})
	ts := toolset(t, tool("slow", func(ctx context.Context, _ string) (string, error) {
		select {
		case <-release:
			return "done", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}))
	d := voice.New(&fakeLive{sess: sess}, ts, allow(), gate.NewMemGrants(), nil)
	wait := run(t, d, dev, nil)

	sess.push(agentkit.LiveToolCall{ID: "c1", Tool: "slow", Args: "{}"})
	sess.push(agentkit.LiveAudio{PCM: []byte{0xAA}})

	// Audio must arrive at the speaker while the tool is still waiting.
	if got := <-dev.played; got[0] != 0xAA {
		t.Errorf("played %v, want 0xAA", got)
	}
	select {
	case r := <-sess.results:
		t.Fatalf("tool answered before it was released: %+v", r)
	default:
	}

	close(release)
	if got := <-sess.results; got.Result != "done" {
		t.Errorf("result = %+v", got)
	}
	sess.Close()
	wait()
}

func TestUnknownTool_IsAnsweredNotFatal(t *testing.T) {
	sess, dev := newSession(), newDevice()
	d := voice.New(&fakeLive{sess: sess}, toolset(t), allow(), gate.NewMemGrants(), nil)
	wait := run(t, d, dev, nil)

	sess.push(agentkit.LiveToolCall{ID: "c1", Tool: "nope", Args: "{}"})
	if got := <-sess.results; got.Err == nil {
		t.Error("unknown tool answered without an error")
	}
	// The session must still be alive.
	sess.push(agentkit.LiveAudio{PCM: []byte{0x01}})
	<-dev.played
	sess.Close()
	wait()
}

// Only what the caller caged may be declared — the cage is the boundary, and a tool that is never
// named cannot be called at all.
func TestOnlyCagedToolsAreDeclared(t *testing.T) {
	sess, dev := newSession(), newDevice()
	live := &fakeLive{sess: sess}
	ts := toolset(t,
		tool("file_read", func(context.Context, string) (string, error) { return "", nil }),
		tool("time_now", func(context.Context, string) (string, error) { return "", nil }),
	)
	d := voice.New(live, ts, allow(), gate.NewMemGrants(), nil)
	wait := run(t, d, dev, nil)
	sess.Close()
	wait() // live.spec is written by the driver's goroutine; read it only once Run has returned

	names := map[string]bool{}
	for _, s := range live.spec {
		names[s.Name] = true
	}
	if len(names) != 2 || !names["file_read"] || !names["time_now"] {
		t.Errorf("declared %v, want exactly the caged two", names)
	}
}

func TestBargeIn_FlushesTheDeviceAndDropsTheHalfSpokenReply(t *testing.T) {
	sess, dev := newSession(), newDevice()
	d := voice.New(&fakeLive{sess: sess}, toolset(t), allow(), gate.NewMemGrants(), nil)
	wait := run(t, d, dev, nil)

	sess.push(agentkit.LiveUserText{Text: "what is the "})
	sess.push(agentkit.LiveModelText{Text: "the weather is"})
	sess.push(agentkit.LiveInterrupted{})
	<-dev.interrupts

	sess.push(agentkit.LiveUserText{Text: "time"})
	sess.push(agentkit.LiveTurnDone{})
	sess.Close()
	msgs := wait()

	if len(msgs) != 1 {
		t.Fatalf("transcript = %+v, want one user message", msgs)
	}
	// The interruption cut the ANSWER, not the question: the user's words survive, the model's
	// abandoned half-sentence does not.
	if msgs[0].Role != agentkit.RoleUser || msgs[0].Content != "what is the time" {
		t.Errorf("message = %+v, want the user's full question", msgs[0])
	}
}

func TestTranscript_KeepsSpokenOrderAndSeed(t *testing.T) {
	sess, dev := newSession(), newDevice()
	d := voice.New(&fakeLive{sess: sess}, toolset(t), allow(), gate.NewMemGrants(), nil)
	seed := []agentkit.Message{{Role: agentkit.RoleUser, Content: "earlier"}}
	wait := run(t, d, dev, seed)

	sess.push(agentkit.LiveUserText{Text: "hello"})
	sess.push(agentkit.LiveModelText{Text: "hi there"})
	sess.push(agentkit.LiveTurnDone{})
	sess.Close()
	msgs := wait()

	var got []string
	for _, m := range msgs {
		got = append(got, string(m.Role)+":"+m.Content)
	}
	want := "user:earlier user:hello assistant:hi there"
	if strings.Join(got, " ") != want {
		t.Errorf("transcript = %q, want %q", strings.Join(got, " "), want)
	}
}

func TestSystemPersona_IsSeededAheadOfHistory(t *testing.T) {
	sess, dev := newSession(), newDevice()
	live := &fakeLive{sess: sess}
	d := voice.New(live, toolset(t), allow(), gate.NewMemGrants(), nil, voice.WithSystem("be brief"))
	wait := run(t, d, dev, []agentkit.Message{{Role: agentkit.RoleUser, Content: "hi"}})

	sess.Close()
	wait()
	if len(live.conv) != 2 || live.conv[0].Role != agentkit.RoleSystem || live.conv[0].Content != "be brief" {
		t.Errorf("seed = %+v, want the persona first", live.conv)
	}
}

// Microphone audio must reach the model unchanged — the satellite already produced the right rate.
func TestMicrophoneAudio_GoesUpstreamVerbatim(t *testing.T) {
	sess, dev := newSession(), newDevice()
	d := voice.New(&fakeLive{sess: sess}, toolset(t), allow(), gate.NewMemGrants(), nil)
	wait := run(t, d, dev, nil)

	dev.mic <- []byte{0x11, 0x22}
	if got := <-sess.audio; got[0] != 0x11 || got[1] != 0x22 {
		t.Errorf("uplink = %v", got)
	}
	sess.Close()
	wait()
}

func TestDeviceDisconnect_EndsTheSession(t *testing.T) {
	sess, dev := newSession(), newDevice()
	d := voice.New(&fakeLive{sess: sess}, toolset(t), allow(), gate.NewMemGrants(), nil)

	done := make(chan error, 1)
	go func() {
		_, err := d.Run(t.Context(), dev, nil)
		done <- err
	}()
	close(dev.mic)

	if err := <-done; err == nil {
		t.Error("Run returned nil after the device disconnected")
	}
}

// The budget is a cost guard, not an error: a session nobody hung up must end on its own, quietly.
func TestBudget_EndsTheSessionWithoutAnError(t *testing.T) {
	sess, dev := newSession(), newDevice()
	d := voice.New(&fakeLive{sess: sess}, toolset(t), allow(), gate.NewMemGrants(), nil,
		voice.WithBudget(20*time.Millisecond))

	done := make(chan error, 1)
	go func() {
		_, err := d.Run(t.Context(), dev, nil)
		done <- err
	}()
	if err := <-done; err != nil {
		t.Errorf("Run = %v, want nil on budget expiry", err)
	}
}

// An answer that arrives while the person is still waiting should cut in — it is what they asked
// for.
func TestToolResult_NotLateWhileTheCallerStillWaits(t *testing.T) {
	sess, dev := newSession(), newDevice()
	ts := toolset(t, tool("time_now", func(context.Context, string) (string, error) { return "12:00", nil }))
	d := voice.New(&fakeLive{sess: sess}, ts, allow(), gate.NewMemGrants(), nil)
	wait := run(t, d, dev, nil)

	sess.push(agentkit.LiveToolCall{ID: "c1", Tool: "time_now", Args: "{}"})
	if got := <-sess.results; got.Late {
		t.Error("result marked late although no turn completed")
	}
	sess.Close()
	wait()
}

// Once the conversation has moved on, the answer must wait for a gap: the person asked about a
// file, then talked about something else, and the file contents cutting into that is worse than a
// short wait.
func TestToolResult_LateWhenTheConversationMovedOn(t *testing.T) {
	sess, dev := newSession(), newDevice()
	release := make(chan struct{})
	ts := toolset(t, tool("slow", func(ctx context.Context, _ string) (string, error) {
		select {
		case <-release:
			return "done", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}))
	obs := newObserver()
	d := voice.New(&fakeLive{sess: sess}, ts, allow(), gate.NewMemGrants(), nil, voice.WithObserver(obs))
	wait := run(t, d, dev, nil)

	sess.push(agentkit.LiveToolCall{ID: "c1", Tool: "slow", Args: "{}"})
	// Two whole turns pass while the approval is outstanding.
	sess.push(agentkit.LiveModelText{Text: "one moment"})
	sess.push(agentkit.LiveTurnDone{})
	sess.push(agentkit.LiveUserText{Text: "anyway, the weather"})
	sess.push(agentkit.LiveTurnDone{})

	// Wait for both turns to be committed, so the counter has certainly advanced before release.
	for range 2 {
		<-obs.said
	}
	close(release)

	if got := <-sess.results; !got.Late {
		t.Error("result not marked late although two turns completed while it ran")
	}
	sess.Close()
	wait()
}
