package speaker

import (
	"context"
	"encoding/json"

	"github.com/efuturetoday/nocturn/agentkit"
)

// WhoAmI is the tool a model calls when the answer depends on who is talking.
//
// Ungated, for the same reason time_now and memory_read are: it hands over no authority and reaches
// nothing. It reports what the microphone already established, and the model could equally have asked
// the person.
//
// It exists because a live session cannot be told anything after it opens — the system instruction
// travels in the setup frame and a second content frame closes the session, so the only channel that
// stays open mid-conversation is a tool result. Asking is therefore the only way the model can learn
// this, and it means nobody has to wait at the start of a session for an answer that is not needed
// until somebody asks something personal.
//
// Most uses should NOT go through here. A tool that acts on a person's behalf reads the speaker off
// the context itself (see FromContext), so the model neither learns whose mailbox it read nor gets
// the chance to name the wrong one. What is left for this tool is what the model genuinely has to
// know: how to address someone, and whether it knows them at all.
func WhoAmI() (agentkit.Tool, error) {
	return agentkit.NewTool("whoami",
		"Report who is currently speaking, as recognised from their voice. Takes no arguments. "+
			`Returns {"name", "confidence"}. An EMPTY name means the speaker is not recognised — `+
			"say so or ask, and do not guess which household member it might be.",
		func(ctx context.Context, _ string) (string, error) {
			out, err := json.Marshal(FromContext(ctx))
			if err != nil {
				return "", err
			}
			return string(out), nil
		},
	)
}
