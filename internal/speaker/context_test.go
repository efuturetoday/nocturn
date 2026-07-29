package speaker_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/efuturetoday/nocturn/internal/speaker"
)

// Every path with no microphone behind it lands here — a typed chat, an agent run, a test — so the
// zero value has to mean "unknown" rather than being a case each caller checks for separately.
func TestFromContextWithoutASpeaker(t *testing.T) {
	if got := speaker.FromContext(context.Background()); got.Known() {
		t.Errorf("an untouched context reported %q", got.Name)
	}
}

// The identity is CONSULTED, not captured: recognition keeps running while a conversation does, and
// a tool called two minutes in should get who is speaking now.
func TestFromContextFollowsChanges(t *testing.T) {
	current := speaker.Identity{Name: "oliver", Confidence: 0.7}
	ctx := speaker.NewContext(context.Background(), func() speaker.Identity { return current })

	if got := speaker.FromContext(ctx); got.Name != "oliver" {
		t.Fatalf("got %q, want oliver", got.Name)
	}
	current = speaker.Identity{Name: "anna", Confidence: 0.8}
	if got := speaker.FromContext(ctx); got.Name != "anna" {
		t.Errorf("got %q after the speaker changed, want anna", got.Name)
	}
}

func TestNewContextWithNil(t *testing.T) {
	ctx := speaker.NewContext(context.Background(), nil)
	if got := speaker.FromContext(ctx); got.Known() {
		t.Errorf("a nil resolver produced %q", got.Name)
	}
}

func TestWhoAmI(t *testing.T) {
	tool, err := speaker.WhoAmI()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("reports the speaker", func(t *testing.T) {
		ctx := speaker.NewContext(context.Background(), func() speaker.Identity {
			return speaker.Identity{Name: "oliver", Confidence: 0.71}
		})
		out, err := tool.Call(ctx, "{}")
		if err != nil {
			t.Fatal(err)
		}
		var got speaker.Identity
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("%q is not valid JSON: %v", out, err)
		}
		if got.Name != "oliver" {
			t.Errorf("reported %q, want oliver", got.Name)
		}
	})

	t.Run("an unknown speaker is an empty name", func(t *testing.T) {
		// Not an error and not a guess: the model is told it does not know, which is what its
		// description tells it to expect.
		out, err := tool.Call(context.Background(), "{}")
		if err != nil {
			t.Fatal(err)
		}
		var got speaker.Identity
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatal(err)
		}
		if got.Known() {
			t.Errorf("reported %q with no speaker installed", got.Name)
		}
	})

	t.Run("the description warns against guessing", func(t *testing.T) {
		// The tool cannot stop a model inventing a name; its description is the only lever, so the
		// warning has to actually be in there.
		if d := tool.Spec().Description; !strings.Contains(d, "not recognised") || !strings.Contains(d, "not guess") {
			t.Errorf("description does not tell the model what an empty name means: %q", d)
		}
	})
}
