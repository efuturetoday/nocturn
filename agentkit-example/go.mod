module github.com/efuturetoday/agentkit-example

go 1.26.1

require (
	github.com/efuturetoday/agentkit v0.0.0
	github.com/efuturetoday/agentkit-openai v0.0.0
)

require github.com/sashabaranov/go-openai v1.41.2 // indirect

replace github.com/efuturetoday/agentkit => ../agentkit

replace github.com/efuturetoday/agentkit-openai => ../agentkit-openai
