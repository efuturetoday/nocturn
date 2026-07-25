package gemini_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/gemini"
)

// fakeTransport scripts the wire: the test pushes server frames onto in and reads what the client
// wrote off sent. No network, no key — the whole protocol is exercised offline.
type fakeTransport struct {
	in     chan []byte
	sent   chan []byte
	closed chan struct{}
	once   sync.Once
}

func newFake() *fakeTransport {
	return &fakeTransport{
		in:     make(chan []byte, 16),
		sent:   make(chan []byte, 16), // buffered: Send must not block the client on a test read
		closed: make(chan struct{}),
	}
}

func (f *fakeTransport) Send(ctx context.Context, frame []byte) error {
	cp := make([]byte, len(frame))
	copy(cp, frame)
	select {
	case f.sent <- cp:
		return nil
	case <-f.closed:
		return errors.New("closed")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *fakeTransport) Recv(ctx context.Context) ([]byte, error) {
	select {
	case b := <-f.in:
		return b, nil
	case <-f.closed:
		return nil, io.EOF
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (f *fakeTransport) Close() error {
	f.once.Do(func() { close(f.closed) })
	return nil
}

// server queues one server frame.
func (f *fakeTransport) server(t *testing.T, v any) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal server frame: %v", err)
	}
	f.in <- raw
}

// nextSent decodes the next client frame into a generic map.
func (f *fakeTransport) nextSent(t *testing.T) map[string]any {
	t.Helper()
	select {
	case raw := <-f.sent:
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("client frame is not JSON: %v", err)
		}
		return m
	case <-f.closed:
		t.Fatal("transport closed before a frame was sent")
		return nil
	}
}

// open runs the setup handshake and returns the live session plus its transport.
func open(t *testing.T, conv []agentkit.Message, tools []agentkit.ToolSpec) (*fakeTransport, agentkit.LiveSession) {
	t.Helper()
	f := newFake()
	f.in <- []byte(`{"setupComplete":{}}`)
	c := gemini.New(func(context.Context, string) (gemini.Transport, error) { return f, nil }, "k", "test-model")
	sess, err := c.Open(t.Context(), conv, tools)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	return f, sess
}

// drain collects events until one of wantType is seen, failing if the stream ends first.
func expect[T agentkit.LiveEvent](t *testing.T, sess agentkit.LiveSession) T {
	t.Helper()
	for ev := range sess.Events() {
		if got, ok := ev.(T); ok {
			return got
		}
	}
	var zero T
	t.Fatalf("stream ended before a %T arrived", zero)
	return zero
}

func TestOpen_SendsSetupWithModelToolsAndTranscription(t *testing.T) {
	spec := agentkit.ToolSpec{
		Name:        "file_read",
		Description: "read a file",
		Parameters:  agentkit.Object(agentkit.Prop("path", agentkit.String("the path"))).Require("path"),
	}
	f, _ := open(t, nil, []agentkit.ToolSpec{spec})

	setup, ok := f.nextSent(t)["setup"].(map[string]any)
	if !ok {
		t.Fatal("first frame is not setup")
	}
	if setup["model"] != "models/test-model" {
		t.Errorf("model = %v, want models/test-model", setup["model"])
	}
	// Both transcription blocks must be present, or the consumer gets audio it cannot persist.
	if _, ok := setup["inputAudioTranscription"]; !ok {
		t.Error("inputAudioTranscription not requested")
	}
	if _, ok := setup["outputAudioTranscription"]; !ok {
		t.Error("outputAudioTranscription not requested")
	}

	decls := setup["tools"].([]any)[0].(map[string]any)["functionDeclarations"].([]any)
	fn := decls[0].(map[string]any)
	if fn["name"] != "file_read" {
		t.Errorf("declared name = %v", fn["name"])
	}
	// Gemini's type enum is uppercase — lowercase is rejected.
	params := fn["parameters"].(map[string]any)
	if params["type"] != "OBJECT" {
		t.Errorf("parameters type = %v, want OBJECT", params["type"])
	}
	props := params["properties"].(map[string]any)["path"].(map[string]any)
	if props["type"] != "STRING" {
		t.Errorf("property type = %v, want STRING", props["type"])
	}
}

func TestOpen_SystemGoesToInstructionHistoryToTurns(t *testing.T) {
	conv := []agentkit.Message{
		{Role: agentkit.RoleSystem, Content: "be brief"},
		{Role: agentkit.RoleUser, Content: "hi"},
		{Role: agentkit.RoleAssistant, Content: "hello"},
		{Role: agentkit.RoleTool, Content: "tool noise", ToolCallID: "1"},
	}
	f, _ := open(t, conv, nil)

	setup := f.nextSent(t)["setup"].(map[string]any)
	instr := setup["systemInstruction"].(map[string]any)["parts"].([]any)[0].(map[string]any)
	if instr["text"] != "be brief" {
		t.Errorf("systemInstruction = %v", instr["text"])
	}

	cc := f.nextSent(t)["clientContent"].(map[string]any)
	turns := cc["turns"].([]any)
	// The tool-result message must NOT replay: only what was actually said belongs in a live seed.
	if len(turns) != 2 {
		t.Fatalf("seeded %d turns, want 2 (user + model)", len(turns))
	}
	if r := turns[1].(map[string]any)["role"]; r != "model" {
		t.Errorf("assistant seeded as %v, want model", r)
	}
	// Seeding must not ask for a reply, or the speaker starts talking by itself on resume.
	if cc["turnComplete"] != false {
		t.Errorf("turnComplete = %v, want false", cc["turnComplete"])
	}
}

func TestSendAudio_WrapsPCMAsBase64WithInputRate(t *testing.T) {
	f, sess := open(t, nil, nil)
	f.nextSent(t) // setup

	pcm := []byte{0x01, 0x02, 0x03, 0x04}
	if err := sess.SendAudio(t.Context(), pcm); err != nil {
		t.Fatalf("SendAudio: %v", err)
	}
	audio := f.nextSent(t)["realtimeInput"].(map[string]any)["audio"].(map[string]any)
	if audio["mimeType"] != "audio/pcm;rate=16000" {
		t.Errorf("mimeType = %v", audio["mimeType"])
	}
	got, err := base64.StdEncoding.DecodeString(audio["data"].(string))
	if err != nil {
		t.Fatalf("data is not base64: %v", err)
	}
	if string(got) != string(pcm) {
		t.Errorf("round-tripped PCM = %v, want %v", got, pcm)
	}
}

func TestSendAudio_EmptyChunkIsNotSent(t *testing.T) {
	f, sess := open(t, nil, nil)
	f.nextSent(t) // setup

	if err := sess.SendAudio(t.Context(), nil); err != nil {
		t.Fatalf("SendAudio(nil): %v", err)
	}
	select {
	case raw := <-f.sent:
		t.Fatalf("empty chunk produced a frame: %s", raw)
	default:
	}
}

func TestToolCall_SurfacesRawArgs(t *testing.T) {
	f, sess := open(t, nil, nil)
	f.server(t, map[string]any{"toolCall": map[string]any{
		"functionCalls": []any{map[string]any{"id": "c1", "name": "http_read", "args": map[string]any{"url": "https://x"}}},
	}})

	call := expect[agentkit.LiveToolCall](t, sess)
	if call.ID != "c1" || call.Tool != "http_read" {
		t.Errorf("call = %+v", call)
	}
	// Args must stay raw JSON: agentkit.Tool.Call takes exactly that.
	var args map[string]string
	if err := json.Unmarshal([]byte(call.Args), &args); err != nil {
		t.Fatalf("args are not JSON: %v", err)
	}
	if args["url"] != "https://x" {
		t.Errorf("args = %v", args)
	}
}

func TestToolCall_MissingArgsBecomesEmptyObject(t *testing.T) {
	f, sess := open(t, nil, nil)
	f.server(t, map[string]any{"toolCall": map[string]any{
		"functionCalls": []any{map[string]any{"id": "c1", "name": "time_now"}},
	}})

	// A no-arg tool must still receive valid JSON, not "".
	if got := expect[agentkit.LiveToolCall](t, sess).Args; got != "{}" {
		t.Errorf("Args = %q, want {}", got)
	}
}

func TestSendResult_ShapesFunctionResponse(t *testing.T) {
	f, sess := open(t, nil, nil)
	f.nextSent(t) // setup

	if err := sess.SendResult(t.Context(), agentkit.ToolResult{ID: "c1", Tool: "time_now", Result: "12:00"}); err != nil {
		t.Fatalf("SendResult: %v", err)
	}
	fr := f.nextSent(t)["toolResponse"].(map[string]any)["functionResponses"].([]any)[0].(map[string]any)
	if fr["id"] != "c1" || fr["name"] != "time_now" {
		t.Errorf("functionResponse = %v", fr)
	}
	if got := fr["response"].(map[string]any)["result"]; got != "12:00" {
		t.Errorf("result = %v", got)
	}
}

func TestSendResult_ErrorReachesTheModel(t *testing.T) {
	f, sess := open(t, nil, nil)
	f.nextSent(t) // setup

	err := sess.SendResult(t.Context(), agentkit.ToolResult{ID: "c1", Tool: "http_read", Err: errors.New("gate: denied")})
	if err != nil {
		t.Fatalf("SendResult: %v", err)
	}
	fr := f.nextSent(t)["toolResponse"].(map[string]any)["functionResponses"].([]any)[0].(map[string]any)
	resp := fr["response"].(map[string]any)
	// A denial is information the model must act on — it may not be swallowed, or the conversation
	// hangs on a call that is never answered.
	if resp["error"] != "gate: denied" {
		t.Errorf("response = %v, want the error surfaced", resp)
	}
}

func TestServerContent_TranscriptsAudioAndTurnEnd(t *testing.T) {
	f, sess := open(t, nil, nil)
	f.server(t, map[string]any{"serverContent": map[string]any{
		"inputTranscription": map[string]any{"text": "what time is it"},
	}})
	if got := expect[agentkit.LiveUserText](t, sess).Text; got != "what time is it" {
		t.Errorf("user text = %q", got)
	}

	f.server(t, map[string]any{"serverContent": map[string]any{
		"outputTranscription": map[string]any{"text": "noon"},
		"modelTurn": map[string]any{"parts": []any{
			map[string]any{"inlineData": map[string]any{
				"mimeType": "audio/pcm;rate=24000",
				"data":     base64.StdEncoding.EncodeToString([]byte{0x10, 0x20}),
			}},
		}},
		"turnComplete": true,
	}})
	if got := expect[agentkit.LiveModelText](t, sess).Text; got != "noon" {
		t.Errorf("model text = %q", got)
	}
	if got := expect[agentkit.LiveAudio](t, sess).PCM; string(got) != string([]byte{0x10, 0x20}) {
		t.Errorf("audio = %v", got)
	}
	expect[agentkit.LiveTurnDone](t, sess)
}

func TestInterrupted_ArrivesBeforeThatFramesAudio(t *testing.T) {
	f, sess := open(t, nil, nil)
	f.server(t, map[string]any{"serverContent": map[string]any{
		"interrupted": true,
		"modelTurn": map[string]any{"parts": []any{
			map[string]any{"inlineData": map[string]any{"data": base64.StdEncoding.EncodeToString([]byte{0x99})}},
		}},
	}})

	// Ordering is the contract: a consumer flushes its playback queue on LiveInterrupted, so audio
	// from the same frame arriving FIRST would be dropped by the very flush it precedes.
	if _, ok := (<-sess.Events()).(agentkit.LiveInterrupted); !ok {
		t.Fatal("first event is not LiveInterrupted")
	}
	if _, ok := (<-sess.Events()).(agentkit.LiveAudio); !ok {
		t.Fatal("second event is not LiveAudio")
	}
}

func TestGoAway_EndsTheStreamWithAnError(t *testing.T) {
	f, sess := open(t, nil, nil)
	f.server(t, map[string]any{"goAway": map[string]any{"timeLeft": "5s"}})

	expect[agentkit.LiveError](t, sess)
	if _, open := <-sess.Events(); open {
		t.Error("stream still open after goAway")
	}
}

func TestUndecodableFrame_IsSkippedNotFatal(t *testing.T) {
	f, sess := open(t, nil, nil)
	f.in <- []byte(`{ this is not json`)
	f.server(t, map[string]any{"serverContent": map[string]any{
		"outputTranscription": map[string]any{"text": "still here"},
	}})

	// One malformed frame must not kill a live conversation.
	if got := expect[agentkit.LiveModelText](t, sess).Text; got != "still here" {
		t.Errorf("model text = %q", got)
	}
}

func TestClose_IsIdempotentAndStopsSends(t *testing.T) {
	_, sess := open(t, nil, nil)
	if err := sess.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if err := sess.SendAudio(t.Context(), []byte{1, 2}); err == nil {
		t.Error("SendAudio on a closed session returned nil")
	}
}

func TestOpen_RequiresAModel(t *testing.T) {
	c := gemini.New(func(context.Context, string) (gemini.Transport, error) { return newFake(), nil }, "k", "")
	if _, err := c.Open(t.Context(), nil, nil); err == nil {
		t.Error("Open with no model returned nil error")
	}
}

func TestOpen_FailsWhenSetupIsNotAcknowledged(t *testing.T) {
	f := newFake()
	f.in <- []byte(`{"serverContent":{"turnComplete":true}}`) // anything but setupComplete
	c := gemini.New(func(context.Context, string) (gemini.Transport, error) { return f, nil }, "k", "m")
	if _, err := c.Open(t.Context(), nil, nil); err == nil {
		t.Error("Open succeeded without setupComplete")
	}
}
