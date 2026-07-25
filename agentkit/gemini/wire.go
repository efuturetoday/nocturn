package gemini

import "encoding/json"

// The BidiGenerateContent wire format, as much of it as a voice session needs. Only the fields this
// adapter sends or reads are modelled: an unknown field on an incoming message is ignored by
// encoding/json, so the protocol can grow without breaking us.

// clientMessage is the client-to-server envelope. Exactly one field is set per frame.
type clientMessage struct {
	Setup         *setup         `json:"setup,omitempty"`
	ClientContent *clientContent `json:"clientContent,omitempty"`
	RealtimeInput *realtimeInput `json:"realtimeInput,omitempty"`
	ToolResponse  *toolResponse  `json:"toolResponse,omitempty"`
}

// setup is the mandatory first frame; the server answers it with setupComplete before accepting
// anything else.
type setup struct {
	Model             string            `json:"model"` // "models/<id>"
	GenerationConfig  *generationConfig `json:"generationConfig,omitempty"`
	SystemInstruction *content          `json:"systemInstruction,omitempty"`
	Tools             []toolDecl        `json:"tools,omitempty"`
	// Both transcription blocks are empty objects that switch the feature on. They are what lets a
	// consumer persist the conversation as text without running its own speech recognition.
	InputAudioTranscription  *struct{} `json:"inputAudioTranscription,omitempty"`
	OutputAudioTranscription *struct{} `json:"outputAudioTranscription,omitempty"`
}

type generationConfig struct {
	ResponseModalities []string `json:"responseModalities,omitempty"` // "AUDIO" or "TEXT", one only
	SpeechConfig       *speech  `json:"speechConfig,omitempty"`
}

type speech struct {
	VoiceConfig *voiceConfig `json:"voiceConfig,omitempty"`
}

type voiceConfig struct {
	PrebuiltVoiceConfig *prebuiltVoice `json:"prebuiltVoiceConfig,omitempty"`
}

type prebuiltVoice struct {
	VoiceName string `json:"voiceName"`
}

type toolDecl struct {
	FunctionDeclarations []functionDecl `json:"functionDeclarations,omitempty"`
}

type functionDecl struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
	// Behavior "NON_BLOCKING" lets the conversation continue while the call runs. The default is
	// blocking: the API pauses ALL model interaction until the response arrives.
	Behavior string `json:"behavior,omitempty"`
}

// clientContent seeds prior conversation. Sent with TurnComplete false it primes context WITHOUT
// asking for a reply — which is what resuming a transcript needs.
type clientContent struct {
	Turns        []content `json:"turns,omitempty"`
	TurnComplete bool      `json:"turnComplete"`
}

type content struct {
	Role  string `json:"role,omitempty"` // "user" | "model"
	Parts []part `json:"parts,omitempty"`
}

type part struct {
	Text       string `json:"text,omitempty"`
	InlineData *blob  `json:"inlineData,omitempty"`
}

type blob struct {
	MimeType string `json:"mimeType,omitempty"`
	Data     string `json:"data,omitempty"` // base64
}

// realtimeInput carries continuous microphone audio, and text on the same footing. Unlike
// clientContent it has no turn semantics: the server's own voice-activity detection decides where
// turns begin and end, which is what makes barge-in work.
//
// It is also the ONLY way to reach the model mid-session on current live models — clientContent is
// restricted there to seeding the initial history, and using it later closes the connection with a
// policy violation.
type realtimeInput struct {
	Audio *blob  `json:"audio,omitempty"`
	Text  string `json:"text,omitempty"`
}

type toolResponse struct {
	FunctionResponses []functionResponse `json:"functionResponses,omitempty"`
}

type functionResponse struct {
	ID       string         `json:"id,omitempty"`
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

// serverMessage is the server-to-client envelope. Like the client side, exactly one field is
// meaningful per frame.
type serverMessage struct {
	SetupComplete *struct{}       `json:"setupComplete,omitempty"`
	ServerContent *serverContent  `json:"serverContent,omitempty"`
	ToolCall      *serverToolCall `json:"toolCall,omitempty"`
	GoAway        *goAway         `json:"goAway,omitempty"`
}

type serverContent struct {
	ModelTurn           *content       `json:"modelTurn,omitempty"`
	InputTranscription  *transcription `json:"inputTranscription,omitempty"`
	OutputTranscription *transcription `json:"outputTranscription,omitempty"`
	Interrupted         bool           `json:"interrupted,omitempty"`
	TurnComplete        bool           `json:"turnComplete,omitempty"`
}

type transcription struct {
	Text string `json:"text,omitempty"`
}

type serverToolCall struct {
	FunctionCalls []functionCall `json:"functionCalls,omitempty"`
}

// functionCall carries Args as RawMessage: agentkit.Tool.Call takes raw JSON, so decoding the
// object only to re-encode it would be pure loss.
type functionCall struct {
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

// goAway warns that the server is about to close the connection. Surfaced as an error so the
// consumer can end the session cleanly instead of seeing a bare read failure.
type goAway struct {
	TimeLeft string `json:"timeLeft,omitempty"`
}
