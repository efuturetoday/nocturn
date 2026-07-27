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
