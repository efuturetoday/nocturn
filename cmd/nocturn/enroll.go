package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/efuturetoday/nocturn/internal/auth"
)

// The enrolment command: ask a satellite to record its own microphone, so a voice can be enrolled
// through the channel that will later have to recognise it.
//
// It is a client of the running daemon rather than something that opens a workspace and does the work
// itself, because the microphone is not here — it is in a device on the network, holding a socket the
// daemon owns. What this contributes is the asking.

// cliBearerFile is where the daemon leaves a credential for this command, beside the device registry
// it was minted from. Bearers are stored in that registry only as a hash, so one cannot be read back
// out of it; a file is the handoff.
//
// The DAEMON writes it and this command only reads it, because the registry is held in memory by
// whoever opened it: a device minted by a second process lands on disk and is invisible to the
// daemon already running, which answers the connection with 4401 and no clue why. One writer.
const cliBearerFile = "./nocturn-data/cli-bearer"

// captureLimit mirrors the ceiling the board enforces on itself. Asking for longer would be answered
// by a device that stops anyway, so it is refused here where the message is useful.
const captureLimit = 120 * time.Second

// cmdEnroll runs one recording: start, wait, stop.
func cmdEnroll(addr, device string, seconds int) int {
	if device == "" {
		fmt.Fprintln(os.Stderr, "enroll: --device is required (the satellite that should record)")
		return 2
	}
	if d := time.Duration(seconds) * time.Second; d <= 0 || d > captureLimit {
		fmt.Fprintf(os.Stderr, "enroll: --seconds must be between 1 and %d\n", int(captureLimit.Seconds()))
		return 2
	}

	bearer, err := cliBearer()
	if err != nil {
		fmt.Fprintln(os.Stderr, "enroll:", err)
		return 1
	}

	// Interrupt has to reach the stop below, not kill the process: a caller that gives up must still
	// leave the microphone off. The board's own timeout is the backstop for the ways that can fail.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	conn, _, err := websocket.Dial(ctx, wsURL(addr), &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer " + bearer}},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "enroll: cannot reach the daemon at %s: %v\n", addr, err)
		return 1
	}
	defer conn.CloseNow()

	send := func(cmd string) error {
		body, err := json.Marshal(map[string]string{"cmd": cmd, "device": device})
		if err != nil {
			return err
		}
		// A bounded write even for the stop below, whose context may already be cancelled.
		w, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return conn.Write(w, websocket.MessageText, body)
	}

	if err := send("capture.start"); err != nil {
		fmt.Fprintln(os.Stderr, "enroll: asking to start:", err)
		return 1
	}
	fmt.Printf("recording %s for %ds — speak normally, and vary what you say\n", device, seconds)

	select {
	case <-time.After(time.Duration(seconds) * time.Second):
	case <-ctx.Done():
		fmt.Println("\nstopping early")
	}

	if err := send("capture.stop"); err != nil {
		fmt.Fprintln(os.Stderr, "enroll: asking to stop:", err)
		fmt.Fprintf(os.Stderr, "enroll: the board stops on its own within %s\n", captureLimit)
		return 1
	}
	fmt.Println("done — recordings are wherever NOCTURN_VOICE_CAPTURE points the daemon")
	return 0
}

// wsURL turns a listen address into the daemon's WebSocket endpoint, accepting the same shorthand
// the serve command prints (":8080", "host:8080", or a full URL).
func wsURL(addr string) string {
	switch {
	case strings.HasPrefix(addr, "ws://"), strings.HasPrefix(addr, "wss://"):
		return addr
	case strings.HasPrefix(addr, "http://"):
		return "ws://" + strings.TrimPrefix(addr, "http://")
	case strings.HasPrefix(addr, "https://"):
		return "wss://" + strings.TrimPrefix(addr, "https://")
	}
	if strings.HasPrefix(addr, ":") {
		addr = "localhost" + addr
	}
	return "ws://" + addr + "/ws"
}

// cliBearer reads the credential the daemon left for this command.
func cliBearer() (string, error) {
	b, err := os.ReadFile(cliBearerFile)
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("no credential at %s — start the daemon once (`nocturn serve`), which writes it", cliBearerFile)
	}
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", cliBearerFile, err)
	}
	token := strings.TrimSpace(string(b))
	if token == "" {
		return "", fmt.Errorf("%s is empty — delete it and restart the daemon", cliBearerFile)
	}
	return token, nil
}

// ensureCLICredential gives the local command line a way in. The DAEMON calls it — at startup, and
// again whenever the device registry is edited from the wire (serve.OnDevicesChanged) — because the
// daemon owns the registry.
//
// It is idempotent by design, and that is what makes the second caller safe: a credential still in
// the registry is left exactly as it is, so the common case costs one lookup. When the row is gone —
// someone forgot it from a phone or a browser — a fresh one is minted and the file rewritten, so
// revoking the command line ROTATES it rather than disabling it until the next restart.
//
// Rotating is the honest meaning. The reason to revoke this credential is that the file leaked, and
// the leaked copy is dead either way; whoever can read the replacement could have read the original,
// because the authority it carries is exactly "can read this directory".
//
// The device is auth.ClassTool: it may ask for a recording and mint a pairing code, and nothing else —
// notably it cannot approve a gated action. That is not much of a concession, since whoever can run
// this binary can already read the workspace off the disk; the point is that the credential lying in
// that directory does not quietly become one that could answer an approval.
func ensureCLICredential(devices *auth.Store, log *slog.Logger) {
	if b, err := os.ReadFile(cliBearerFile); err == nil {
		if token := strings.TrimSpace(string(b)); token != "" {
			if _, ok := devices.Lookup(token); ok {
				return // still valid for this registry
			}
		}
	}
	token, err := devices.Mint("cli", auth.ClassTool)
	if err != nil {
		log.Error("could not enrol the command line", "err", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(cliBearerFile), 0o700); err != nil {
		log.Error("could not create the credential directory", "err", err)
		return
	}
	if err := os.WriteFile(cliBearerFile, []byte(token+"\n"), 0o600); err != nil {
		log.Error("could not save the command line's credential", "err", err)
		return
	}
	log.Info("wrote a credential for the local command line", "file", cliBearerFile)
}
