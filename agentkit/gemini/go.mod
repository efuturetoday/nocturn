module github.com/efuturetoday/nocturn/agentkit/gemini

go 1.26.1

require github.com/efuturetoday/nocturn/agentkit v0.0.0

// agentkit is an unpublished sibling module in this repo; resolve it locally. (go.work also links
// it for editing; this keeps `go build ./agentkit/gemini` working outside the workspace too.)

replace github.com/efuturetoday/nocturn/agentkit => ..
