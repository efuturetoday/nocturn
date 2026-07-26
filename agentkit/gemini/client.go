// Package gemini adapts the Gemini Live API (BidiGenerateContent) to agentkit.LiveLLM: a duplex
// audio session with native function calling and server-side voice activity detection.
//
// It speaks the protocol directly over an injected Transport instead of importing a vendor SDK.
// Gemini Live is JSON over a WebSocket — small enough that a client is cheaper than a dependency,
// and keeping this module dependency-free preserves agentkit's ability to move to its own
// repository. It also makes the whole protocol testable offline against a scripted fake transport.
//
// Audio contract: input is raw little-endian 16-bit PCM, natively 16 kHz (the rate is declared in
// the MIME type, so the server resamples other rates). Output is ALWAYS 24 kHz — a consumer feeding
// a fixed-rate sink must resample.
package gemini

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/efuturetoday/nocturn/agentkit"
)

// endpoint is the Live API's WebSocket path. The key rides in the query string because the
// WebSocket handshake carries no Authorization header we can set portably.
const endpoint = "wss://generativelanguage.googleapis.com/ws/" +
	"google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent"

// inputMIME declares the microphone format. 16 kHz is the API's native input rate and exactly what
// ESP-SR's audio front end and a browser AudioContext both produce.
const inputMIME = "audio/pcm;rate=16000"

// OutputSampleRate is the rate of every LiveAudio chunk this adapter emits. Fixed by the API,
// exported because a consumer must resample or configure its sink to match.
const OutputSampleRate = 24000

// eventBuffer bounds the event channel. Audio arrives in small chunks and a stalled consumer would
// otherwise grow this without limit; a full channel blocks the reader, which is the correct
// backpressure — better a stalled session than an unbounded queue.
const eventBuffer = 64

// Client is a LiveLLM backed by the Gemini Live API. It holds no connection: each Open dials its
// own, so one Client serves many concurrent sessions.
type Client struct {
	dial   Dialer
	apiKey string
	model  string
	voice  string
	log    agentkit.Logger
}

// Option configures a Client.
type Option func(*Client)

// WithLogger sets the diagnostic logger (default: agentkit.NopLogger()). A nil logger is ignored.
func WithLogger(l agentkit.Logger) Option {
	return func(c *Client) {
		if l != nil {
			c.log = l
		}
	}
}

// WithVoice selects a prebuilt voice by name (provider-defined; empty = the API default).
func WithVoice(name string) Option {
	return func(c *Client) { c.voice = name }
}

// New builds a Client that dials with d, authenticates with apiKey and runs model (the bare id,
// e.g. "gemini-live-2.5-flash-preview" — the "models/" prefix is added). The model is required
// rather than defaulted: live-capable model ids churn, and a stale built-in default would fail at
// connect time with a confusing error instead of at configuration time.
func New(d Dialer, apiKey, model string, opts ...Option) *Client {
	c := &Client{dial: d, apiKey: apiKey, model: model, log: agentkit.NopLogger()}
	for _, o := range opts {
		o(c)
	}
	return c
}

var _ agentkit.LiveLLM = (*Client)(nil)

// Open dials a session, sends the setup frame, waits for setupComplete, seeds any prior transcript,
// and starts the reader. The returned session is live until Close or ctx cancellation.
func (c *Client) Open(ctx context.Context, conv []agentkit.Message, tools []agentkit.ToolSpec) (agentkit.LiveSession, error) {
	if c.model == "" {
		return nil, errors.New("gemini: no model configured")
	}
	tr, err := c.dial(ctx, endpoint+"?key="+c.apiKey)
	if err != nil {
		return nil, fmt.Errorf("gemini: dial: %w", err)
	}

	system, turns := split(conv)
	su := &setup{
		Model:                    "models/" + strings.TrimPrefix(c.model, "models/"),
		GenerationConfig:         &generationConfig{ResponseModalities: []string{"AUDIO"}},
		Tools:                    declare(tools),
		InputAudioTranscription:  &struct{}{},
		OutputAudioTranscription: &struct{}{},
	}
	if c.voice != "" {
		su.GenerationConfig.SpeechConfig = &speech{
			VoiceConfig: &voiceConfig{PrebuiltVoiceConfig: &prebuiltVoice{VoiceName: c.voice}},
		}
	}
	if system != "" {
		su.SystemInstruction = &content{Parts: []part{{Text: system}}}
	}
	if err := send(ctx, tr, clientMessage{Setup: su}); err != nil {
		tr.Close()
		return nil, fmt.Errorf("gemini: setup: %w", err)
	}
	if err := awaitSetup(ctx, tr); err != nil {
		tr.Close()
		return nil, err
	}
	// Seed prior conversation with TurnComplete false: it primes context without asking the model to
	// answer, so resuming a transcript does not make the speaker start talking on its own.
	//
	// This is the ONLY clientContent frame the adapter ever sends. Live models restrict it to exactly
	// this — seeding initial history — and a later one is answered with a policy violation that
	// closes the session. Everything mid-session goes through realtimeInput instead.
	if len(turns) > 0 {
		if err := send(ctx, tr, clientMessage{ClientContent: &clientContent{Turns: turns}}); err != nil {
			tr.Close()
			return nil, fmt.Errorf("gemini: seed history: %w", err)
		}
	}

	s := &session{
		tr:     tr,
		log:    c.log,
		events: make(chan agentkit.LiveEvent, eventBuffer),
		done:   make(chan struct{}),
	}
	go s.read(ctx)
	return s, nil
}

// awaitSetup blocks until the server acknowledges setup. Anything else arriving first is a protocol
// error worth failing on: sending audio before setupComplete is rejected by the server anyway.
func awaitSetup(ctx context.Context, tr Transport) error {
	raw, err := tr.Recv(ctx)
	if err != nil {
		return fmt.Errorf("gemini: awaiting setup: %w", err)
	}
	var msg serverMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return fmt.Errorf("gemini: awaiting setup: bad frame: %w", err)
	}
	if msg.SetupComplete == nil {
		return fmt.Errorf("gemini: expected setupComplete, got %s", summarize(raw))
	}
	return nil
}

// split separates the system instruction from the replayable turns. Gemini carries the system
// prompt in its own setup field rather than as a turn, and tool-result messages have no place in a
// seeded live transcript — only what was actually said replays.
func split(conv []agentkit.Message) (system string, turns []content) {
	var sys []string
	for _, m := range conv {
		switch m.Role {
		case agentkit.RoleSystem:
			if m.Content != "" {
				sys = append(sys, m.Content)
			}
		case agentkit.RoleUser:
			if m.Content != "" {
				turns = append(turns, content{Role: "user", Parts: []part{{Text: m.Content}}})
			}
		case agentkit.RoleAssistant:
			if m.Content != "" {
				turns = append(turns, content{Role: "model", Parts: []part{{Text: m.Content}}})
			}
		}
	}
	return strings.Join(sys, "\n\n"), turns
}

// session is one open Live connection.
type session struct {
	tr     Transport
	log    agentkit.Logger
	events chan agentkit.LiveEvent

	mu       sync.Mutex // serializes writes: Transport guarantees one concurrent Send only
	closeOne sync.Once
	done     chan struct{}
}

var _ agentkit.LiveSession = (*session)(nil)

func (s *session) Events() <-chan agentkit.LiveEvent { return s.events }

// SendAudio forwards one PCM chunk. The bytes are base64'd into the JSON frame — the Live API has
// no binary framing, so this encoding is the protocol, not a design choice.
func (s *session) SendAudio(ctx context.Context, pcm []byte) error {
	if len(pcm) == 0 {
		return nil
	}
	return s.write(ctx, clientMessage{RealtimeInput: &realtimeInput{
		Audio: &blob{MimeType: inputMIME, Data: base64.StdEncoding.EncodeToString(pcm)},
	}})
}

// A non-blocking declaration requires each response to say WHEN the model should surface it.
//
//   - interrupt — report it now, cutting into whatever is being said. Right while the person is
//     still waiting: the outcome, allowed or denied, is the thing they asked for.
//   - whenIdle — hold it until the model finishes what it is doing. Right once the conversation has
//     moved on, so an answer from two subjects ago does not cut into the current one.
//
// SILENT, the third option, is never used here: it files the result away without telling anyone,
// which would leave a person who approved something on their phone with no confirmation that it
// happened.
const (
	interrupt = "INTERRUPT"
	whenIdle  = "WHEN_IDLE"
)

// SendResult answers one tool call. A failed tool reports its error as the result payload: the
// model is meant to react to it (apologize, try another route), so swallowing it would leave the
// conversation hanging on a call that never gets answered.
//
// An interim result sets willContinue, which turns the call into a generator: the model learns why
// it is waiting, and a final result for the same id follows. Interim results are always WHEN_IDLE —
// they are an aside, and cutting the model off to deliver one is the very rudeness they exist to
// avoid. For the final result lateness picks the scheduling instead of the outcome: a denial the
// person is still waiting on should interrupt, a success they have long moved past should not.
func (s *session) SendResult(ctx context.Context, r agentkit.ToolResult) error {
	scheduling := interrupt
	switch {
	case r.Pending, r.Late:
		scheduling = whenIdle
	}
	payload := map[string]any{"result": r.Result}
	if r.Err != nil {
		payload = map[string]any{"error": r.Err.Error()}
	}
	fr := functionResponse{ID: r.ID, Name: r.Tool, Response: payload, Scheduling: scheduling}
	if r.Pending {
		fr.WillContinue = &yes
	}
	return s.write(ctx, clientMessage{ToolResponse: &toolResponse{FunctionResponses: []functionResponse{fr}}})
}

// yes is the addressable true willContinue points at. A final result leaves the field unset rather
// than sending false: both end the call, and omitting it keeps the frame to what it means.
var yes = true

func (s *session) write(ctx context.Context, msg clientMessage) error {
	select {
	case <-s.done:
		return errors.New("gemini: session closed")
	default:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return send(ctx, s.tr, msg)
}

// Close ends the session once, regardless of how many callers race on it.
func (s *session) Close() error {
	var err error
	s.closeOne.Do(func() {
		close(s.done)
		err = s.tr.Close()
	})
	return err
}

// read drains the transport onto the event stream until the peer closes, ctx is cancelled, or the
// session is closed. It owns the event channel and is the only goroutine that closes it.
func (s *session) read(ctx context.Context) {
	defer close(s.events)
	for {
		raw, err := s.tr.Recv(ctx)
		if err != nil {
			select {
			case <-s.done: // an expected close, not a failure
			case <-ctx.Done():
			default:
				s.emit(ctx, agentkit.LiveError{Err: err})
			}
			return
		}
		var msg serverMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			s.log.Warn("gemini: undecodable frame", "err", err)
			continue
		}
		if !s.dispatch(ctx, msg) {
			return
		}
	}
}

// dispatch turns one server message into events. It returns false when the session should end.
func (s *session) dispatch(ctx context.Context, msg serverMessage) bool {
	if g := msg.GoAway; g != nil {
		s.emit(ctx, agentkit.LiveError{Err: fmt.Errorf("gemini: server closing (time left %s)", g.TimeLeft)})
		return false
	}
	if tc := msg.ToolCall; tc != nil {
		for _, fc := range tc.FunctionCalls {
			args := string(fc.Args)
			if args == "" {
				args = "{}"
			}
			s.emit(ctx, agentkit.LiveToolCall{ID: fc.ID, Tool: fc.Name, Args: args})
		}
	}
	sc := msg.ServerContent
	if sc == nil {
		return true
	}
	// Interruption first: it invalidates audio the consumer may still be holding, so it must not sit
	// behind this frame's own audio in the stream.
	if sc.Interrupted {
		s.emit(ctx, agentkit.LiveInterrupted{})
	}
	if t := sc.InputTranscription; t != nil && t.Text != "" {
		s.emit(ctx, agentkit.LiveUserText{Text: t.Text})
	}
	if t := sc.OutputTranscription; t != nil && t.Text != "" {
		s.emit(ctx, agentkit.LiveModelText{Text: t.Text})
	}
	if mt := sc.ModelTurn; mt != nil {
		for _, p := range mt.Parts {
			if p.InlineData == nil || p.InlineData.Data == "" {
				continue
			}
			pcm, err := base64.StdEncoding.DecodeString(p.InlineData.Data)
			if err != nil {
				s.log.Warn("gemini: undecodable audio", "err", err)
				continue
			}
			s.emit(ctx, agentkit.LiveAudio{PCM: pcm})
		}
	}
	if sc.TurnComplete {
		s.emit(ctx, agentkit.LiveTurnDone{})
	}
	return true
}

// emit delivers one event, giving up if the session ends or ctx is cancelled while a slow consumer
// blocks the channel.
func (s *session) emit(ctx context.Context, ev agentkit.LiveEvent) {
	select {
	case s.events <- ev:
	case <-s.done:
	case <-ctx.Done():
	}
}

func send(ctx context.Context, tr Transport, msg clientMessage) error {
	frame, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return tr.Send(ctx, frame)
}

// summarize trims a frame for an error message: a Live frame can carry a megabyte of base64 audio,
// which has no place in a log line.
func summarize(raw []byte) string {
	const max = 200
	if len(raw) > max {
		return string(raw[:max]) + "…"
	}
	return string(raw)
}
