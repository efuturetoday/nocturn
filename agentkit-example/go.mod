module github.com/efuturetoday/nocturn/agentkit-example

go 1.26.1

require (
	github.com/efuturetoday/nocturn/agentkit v0.0.0
	github.com/efuturetoday/nocturn/agentkit-gate v0.0.0
	github.com/efuturetoday/nocturn/agentkit-openai v0.0.0
	github.com/efuturetoday/nocturn/agentkit-runtime v0.0.0
	github.com/efuturetoday/nocturn/agentkit-tools v0.0.0
)

require github.com/sashabaranov/go-openai v1.41.2 // indirect

replace github.com/efuturetoday/nocturn/agentkit => ../agentkit

replace github.com/efuturetoday/nocturn/agentkit-openai => ../agentkit-openai

replace github.com/efuturetoday/nocturn/agentkit-gate => ../agentkit-gate

replace github.com/efuturetoday/nocturn/agentkit-runtime => ../agentkit-runtime

replace github.com/efuturetoday/nocturn/agentkit-tools => ../agentkit-tools
