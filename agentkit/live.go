package agentkit

import "context"

// LiveLLM is the duplex counterpart of LLM: a model that consumes and produces a CONTINUOUS audio
// stream rather than answering one turn at a time. It is a separate port on purpose — LLM.Next is
// request/response and has a turn boundary, a live model has neither, so widening LLM would have
// meant a turn-shaped API pretending to be a stream. Session drives LLM; a consumer drives LiveLLM
// (see the driver in nocturn's internal/voice), because what a live stream needs — audio transport,
// barge-in, wall-clock budget — is the consumer's concern, not the turn loop's.
//
// Tools are unaffected: a LiveSession surfaces ToolCall events and takes ToolResult back, so the
// SAME ToolSet (and, in a gated deployment, the same gate machinery on ctx) serves both ports.
type LiveLLM interface {
	// Open starts a session seeded with conv (a system message and any prior transcript) and the
	// tool declarations the model may call. The session runs until Close or ctx cancellation.
	Open(ctx context.Context, conv []Message, tools []ToolSpec) (LiveSession, error)
}

// LiveSession is one open duplex conversation. Send* and Close are safe to call from a goroutine
// other than the one draining Events — that separation is the point: a tool call that blocks on a
// human approval must not stall the audio path.
type LiveSession interface {
	// SendAudio feeds one chunk of microphone PCM upstream. The adapter documents the wire format
	// it expects; nocturn's satellites produce 16 kHz mono little-endian PCM16 end to end.
	SendAudio(ctx context.Context, pcm []byte) error

	// SendResult answers one ToolCall. Results may arrive in any order and long after the call, so
	// the model can keep speaking while a tool waits on an out-of-band approval.
	SendResult(ctx context.Context, r ToolResult) error

	// Events is the session's one-way output stream, closed when the session ends. A consumer that
	// stops draining it stalls the session — drain it in its own goroutine.
	Events() <-chan LiveEvent

	// Close ends the session and releases its transport. It is idempotent.
	Close() error
}

// ToolResult is the answer to a LiveToolCall. Err is rendered for the model rather than returned to
// the caller: a failed tool is information the model acts on, not a transport failure.
type ToolResult struct {
	ID     string
	Tool   string
	Result string
	Err    error

	// Late reports that the conversation moved on while this call was outstanding — the speakers
	// completed at least one turn between the call and this answer.
	//
	// It exists because correlation is not the same as timing. A provider matches the answer to its
	// call by ID however long it takes, but an answer that arrives two subjects later should not cut
	// into whatever is being said now: the person asked about a file, then moved to the weather, and
	// having the file contents interrupt the weather is worse than a short wait. An adapter maps
	// this onto whatever delivery its provider offers; one with no such control ignores it.
	Late bool
}

// LiveEvent is one item on a LiveSession's output stream. Like Event, the set is sealed (unexported
// marker) so consumers switch exhaustively and no foreign variant enters the stream.
type LiveEvent interface{ isLiveEvent() }

// LiveAudio is a chunk of model speech. The adapter documents its sample rate — Gemini Live emits
// 24 kHz regardless of the input rate, so a consumer targeting a fixed-rate sink resamples.
type LiveAudio struct{ PCM []byte }

// LiveUserText is a transcript delta of what the USER said, as the provider heard it. It exists so a
// consumer can persist and display the conversation without running its own speech recognition.
type LiveUserText struct{ Text string }

// LiveModelText is a transcript delta of what the MODEL said. Same purpose as LiveUserText: the
// spoken answer in text, for the transcript and the screen.
type LiveModelText struct{ Text string }

// LiveToolCall is a model-issued call. Args is raw JSON, exactly as Tool.Call expects it.
type LiveToolCall struct {
	ID   string
	Tool string
	Args string
}

// LiveInterrupted reports that the user talked over the model and generation was cut. Any audio the
// consumer has buffered but not yet played is now stale and must be dropped — otherwise the speaker
// keeps answering a question the user already abandoned.
type LiveInterrupted struct{}

// LiveTurnDone marks the model finishing a reply. A live session continues past it; it is a
// punctuation mark for the consumer (flush the transcript, idle the UI), not an end of session.
type LiveTurnDone struct{}

// LiveError is a session-level failure the adapter could not recover from. It is delivered on the
// stream rather than returned, because it can happen at any point after Open; the stream closes
// after it.
type LiveError struct{ Err error }

func (LiveAudio) isLiveEvent()       {}
func (LiveUserText) isLiveEvent()    {}
func (LiveModelText) isLiveEvent()   {}
func (LiveToolCall) isLiveEvent()    {}
func (LiveInterrupted) isLiveEvent() {}
func (LiveTurnDone) isLiveEvent()    {}
func (LiveError) isLiveEvent()       {}
