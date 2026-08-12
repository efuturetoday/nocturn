package serve

import (
	"testing"

	"github.com/efuturetoday/nocturn/internal/auth"
)

// The zero value is a device that may do nothing, so a class nobody taught capabilitiesOf about
// lands on the safe side by construction rather than by anyone remembering to handle it.
func TestCapabilities_UnknownClassMayDoNothing(t *testing.T) {
	for _, class := range []auth.Class{auth.ClassUnknown, "car", "toaster"} {
		if got := capabilitiesOf(class); got.approve || got.enrol {
			t.Errorf("class %q got %+v, want nothing", class, got)
		}
	}
}

// An appliance has no authenticated input path: nobody at it is identified, so nothing it reports is
// anyone's consent. The approval broker takes the FIRST answer, so one that could approve would
// outrace the phone it exists to defer to.
func TestCapabilities_ApplianceMayNotApproveOrEnrol(t *testing.T) {
	if got := capabilitiesOf(auth.ClassAppliance); got.approve || got.enrol {
		t.Errorf("appliance got %+v, want nothing", got)
	}
}

func TestCapabilities_AppMayApproveAndEnrol(t *testing.T) {
	got := capabilitiesOf(auth.ClassApp)
	if !got.approve || !got.enrol {
		t.Errorf("app got %+v, want both", got)
	}
}

// A browser holding the page this daemon served got here the same way the app did, so it answers and
// administers the same way — but it may not reach into an appliance's microphone, which is what keeps
// the class from being a second spelling of ClassApp.
func TestCapabilities_WebMayApproveAndEnrolButNotRecord(t *testing.T) {
	got := capabilitiesOf(auth.ClassWeb)
	if !got.approve || !got.enrol {
		t.Errorf("web got %+v, want approve and enrol", got)
	}
	if got.captureAudio {
		t.Error("web may switch on an appliance's microphone — a browser session has not earned that reach")
	}
	// The consequence that matters: not holding captureAudio means web does not cover tool, so a web
	// bearer cannot mint the class that does hold it.
	if got.covers(capabilitiesOf(auth.ClassTool)) {
		t.Error("a web device covers a tool — it could enrol one and gain captureAudio indirectly")
	}
}

// classFor is the only place a holder is turned into a class, and a platform it has never been told
// about must not be quietly enrolled. Reporting "unrecognised" rather than mapping to ClassUnknown is
// what lets the pairing handlers refuse instead of handing back a bearer that can do nothing.
func TestClassFor_KnownPlatformsAndNothingElse(t *testing.T) {
	known := map[string]auth.Class{
		"ios":     auth.ClassApp,
		"android": auth.ClassApp,
		"web":     auth.ClassWeb,
	}
	for platform, want := range known {
		got, ok := classFor(platform)
		if !ok || got != want {
			t.Errorf("classFor(%q) = %q, %v; want %q, true", platform, got, ok, want)
		}
	}

	// Note "" among these: an omitted platform used to mean ClassApp, and it must not any more.
	for _, platform := range []string{"", "IOS", "windows", "tool", "appliance", "toaster"} {
		got, ok := classFor(platform)
		if ok {
			t.Errorf("classFor(%q) = %q, true; want it reported as unrecognised", platform, got)
		}
		if c := capabilitiesOf(got); c.approve || c.enrol || c.captureAudio {
			t.Errorf("classFor(%q) fell back to %q, which may do %+v", platform, got, c)
		}
	}

	// The two classes that never come from a holder's own say-so. Reaching either from here would be
	// self-enrolment: captureAudio, or a device that exists to be enrolled ON someone's behalf.
	for _, platform := range []string{"tool", "appliance", "cli", "satellite"} {
		if got, _ := classFor(platform); got == auth.ClassTool || got == auth.ClassAppliance {
			t.Errorf("classFor(%q) = %q — that class must only come from POST /devices", platform, got)
		}
	}
}

// Arming a pairing code opens the household from nothing, so it belongs to the one holder whose
// credential already implies holding the workspace: the local command line, whose bearer sits in a
// 0600 file beside the vault.
func TestCapabilities_OnlyTheLocalToolMayArmAPairingCode(t *testing.T) {
	if !capabilitiesOf(auth.ClassTool).bootstrap {
		t.Error("the command line cannot arm a pairing code — `nocturn pair` would be impossible")
	}
	for _, class := range []auth.Class{auth.ClassApp, auth.ClassWeb, auth.ClassAppliance, auth.ClassUnknown} {
		if capabilitiesOf(class).bootstrap {
			t.Errorf("class %q may arm a pairing code; only the local tool should", class)
		}
	}
	// The consequence, and the reason it is not simply given to everything that can enrol: if an app
	// held it too, covers() would say an app covers a tool, and a phone could mint a disk-equivalent
	// credential through POST /devices.
	if capabilitiesOf(auth.ClassApp).covers(capabilitiesOf(auth.ClassTool)) {
		t.Error("an app covers a tool — it could enrol one and gain the authority to reopen the household")
	}
}

// The one predicate behind "does this household still need a pairing code?". serveOn and handleDaemon
// both ask it, and a disagreement between them IS the dead end: a daemon that armed no code while the
// client offers a code field, or the reverse.
func TestHouseholdCanEnrol(t *testing.T) {
	for _, tc := range []struct {
		name    string
		present []auth.Class
		want    bool
	}{
		{"empty", nil, false},
		{"only the cli", []auth.Class{auth.ClassTool}, false},
		{"cli and an appliance", []auth.Class{auth.ClassTool, auth.ClassAppliance}, false},
		{"a phone", []auth.Class{auth.ClassTool, auth.ClassApp}, true},
		{"a browser", []auth.Class{auth.ClassTool, auth.ClassWeb}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := auth.New(t.TempDir() + "/devices.json")
			if err != nil {
				t.Fatalf("device store: %v", err)
			}
			for _, class := range tc.present {
				if _, err := s.Mint("d", class); err != nil {
					t.Fatalf("mint %s: %v", class, err)
				}
			}
			if got := householdCanEnrol(s); got != tc.want {
				t.Errorf("householdCanEnrol = %v, want %v", got, tc.want)
			}
		})
	}
}

// covers is the whole enrolment rule: nobody hands out authority they do not hold.
func TestCovers_NoDeviceMayExceedItself(t *testing.T) {
	app := capabilitiesOf(auth.ClassApp)
	appliance := capabilitiesOf(auth.ClassAppliance)

	if !app.covers(appliance) {
		t.Error("an app cannot enrol an appliance, but it should be able to")
	}
	if appliance.covers(app) {
		t.Error("an appliance may enrol an app — a stolen bearer would escalate")
	}
	if !app.covers(app) {
		t.Error("covers is not reflexive")
	}
	// Deliberate: an appliance covers an appliance, but it never gets that far — enrolling at all
	// requires the enrol capability, which it does not have.
	if !appliance.covers(appliance) {
		t.Error("covers should be reflexive even for the empty set")
	}
}
