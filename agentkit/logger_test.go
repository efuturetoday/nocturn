package agentkit_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/efuturetoday/nocturn/agentkit"
)

func TestSlogLogger_NilYieldsNop(t *testing.T) {
	if reflect.TypeOf(agentkit.SlogLogger(nil)) != reflect.TypeOf(agentkit.NopLogger()) {
		t.Fatal("SlogLogger(nil) is not the NopLogger type")
	}
}

func TestNopLogger_Discards(t *testing.T) {
	l := agentkit.NopLogger()
	l.Debug("d")
	l.Info("i")
	l.Warn("w")
	l.Error("e")
	if l.With("k", "v") == nil {
		t.Fatal("With returned nil")
	}
	if l.WithContext(context.Background()) == nil {
		t.Fatal("WithContext returned nil")
	}
}
