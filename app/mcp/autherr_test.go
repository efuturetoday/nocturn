package mcp_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/efuturetoday/nocturn/app/mcp"
)

// A 401 from an MCP server surfaces as a typed AuthRequiredError whose message tells
// the operator exactly what to run, and which errors.As can pick out of a wrapped chain.
func TestAuthRequiredError(t *testing.T) {
	err := error(&mcp.AuthRequiredError{Server: "github", ResourceMetadata: "https://api.githubcopilot.com/.well-known/oauth-protected-resource"})
	if !strings.Contains(err.Error(), "nocturn auth github") {
		t.Errorf("error message must be actionable: %q", err.Error())
	}
	wrapped := errors.Join(errors.New("connect failed"), err)
	var got *mcp.AuthRequiredError
	if !errors.As(wrapped, &got) || got.Server != "github" {
		t.Errorf("AuthRequiredError must be recoverable via errors.As, got %+v", got)
	}
}
