module github.com/efuturetoday/nocturn

go 1.26.1

require (
	github.com/coder/websocket v1.8.15
	github.com/joho/godotenv v1.5.1
	github.com/libp2p/zeroconf/v2 v2.2.0
	github.com/petar-dambovaliev/aho-corasick v0.0.0-20250424160509-463d218d4745
	github.com/sashabaranov/go-openai v1.42.0 // indirect
	github.com/tetratelabs/wazero v1.12.0
	golang.org/x/crypto v0.55.0
	golang.org/x/net v0.57.0
	golang.org/x/oauth2 v0.36.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/emersion/go-sasl v0.0.0-20241020182733-b788ff22d5a6 // indirect
	github.com/miekg/dns v1.1.43 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
)

require (
	github.com/efuturetoday/nocturn/agentkit v0.0.0
	github.com/efuturetoday/nocturn/agentkit/gate v0.0.0
	github.com/efuturetoday/nocturn/agentkit/gemini v0.0.0
	github.com/efuturetoday/nocturn/agentkit/openai v0.0.0
	github.com/efuturetoday/nocturn/agentkit/runtime v0.0.0
	github.com/emersion/go-imap/v2 v2.0.0-beta.8
	github.com/emersion/go-message v0.18.2
	github.com/grindlemire/go-tui v0.19.0
	github.com/lmittmann/tint v1.2.0
)

replace (
	github.com/efuturetoday/nocturn/agentkit => ./agentkit
	github.com/efuturetoday/nocturn/agentkit/gate => ./agentkit/gate
	github.com/efuturetoday/nocturn/agentkit/openai => ./agentkit/openai
	github.com/efuturetoday/nocturn/agentkit/runtime => ./agentkit/runtime
	github.com/efuturetoday/nocturn/agentkit/tools => ./agentkit/tools
)

replace github.com/efuturetoday/nocturn/agentkit/gemini => ./agentkit/gemini
