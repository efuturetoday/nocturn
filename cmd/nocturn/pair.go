package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// `nocturn pair` — open the door, from the machine that owns the house.
//
// The bootstrap code was armed once, at daemon startup, for five minutes. Every way of missing that
// window led somewhere with no exit: walk away from the terminal and come back after lunch, ssh in a
// week later, mistype the code five times, or simply run the daemon as a service and never see the
// line at all. The recovery was restarting the daemon, or deleting the device registry — which
// unpairs every device you still have.
//
// A credential with a lifetime needs a way to get another one. This is that way, and it is available
// for as long as the daemon runs.
//
// Authority comes from the file, not the network. It reads the same 0600 credential `nocturn enroll`
// uses, which the daemon wrote beside the vault — so being allowed to open the household means being
// allowed to read its directory, and anyone who can do that could read the vault instead. Note what
// is deliberately NOT the rule: "the request came from loopback". That would be strictly weaker than
// the file it imitates, because 0600 keeps a second local user out and 127.0.0.1 does not.

// cmdPair arms a fresh pairing code on the running daemon and prints it.
func cmdPair(addr string, open bool) int {
	bearer, err := cliBearer()
	if err != nil {
		fmt.Fprintln(os.Stderr, "pair:", err)
		return 1
	}

	base := httpBase(addr)
	req, err := http.NewRequest(http.MethodPost, base+"/pair/code", bytes.NewReader([]byte("{}")))
	if err != nil {
		fmt.Fprintln(os.Stderr, "pair:", err)
		return 1
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")

	res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pair: cannot reach the daemon at %s: %v\n", addr, err)
		fmt.Fprintln(os.Stderr, "pair: is it running? `nocturn serve`")
		return 1
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "pair: the daemon refused (%s) %s\n", res.Status, errorBody(res))
		return 1
	}

	var out struct {
		Code             string `json:"code"`
		ExpiresInSeconds int    `json:"expiresInSeconds"`
		Attempts         int    `json:"attempts"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		fmt.Fprintln(os.Stderr, "pair: could not read the daemon's answer:", err)
		return 1
	}

	// The link carries the code in the FRAGMENT, which is never sent to a server and never appears in
	// an access log or a Referer header. The page reads it, redeems it, and scrubs it from the address
	// bar — so the user pairs a browser with one click and nothing durable is left in the URL. A code
	// that has been spent is worth nothing to whoever finds it in history later; it is single-use,
	// five minutes, five attempts.
	link := fmt.Sprintf("%s/#c=%s", base, out.Code)

	fmt.Printf("pairing code: %s\n", out.Code)
	fmt.Printf("valid for %s, %d attempts, single use\n",
		time.Duration(out.ExpiresInSeconds)*time.Second, out.Attempts)
	fmt.Println()
	fmt.Printf("  in a browser:  %s\n", link)
	fmt.Println("  in the app:    enter the code on the pairing screen")
	fmt.Println()
	fmt.Println("Run this again at any time for a new code.")

	if open {
		if err := openBrowser(link); err != nil {
			fmt.Fprintln(os.Stderr, "pair: could not open a browser:", err)
		}
	}
	return 0
}

// httpBase turns a listen address into the daemon's HTTP origin, accepting the same shorthand as
// wsURL — ":8080", "host:8080", or a full URL.
func httpBase(addr string) string {
	switch {
	case strings.HasPrefix(addr, "http://"), strings.HasPrefix(addr, "https://"):
		return strings.TrimSuffix(addr, "/")
	case strings.HasPrefix(addr, "ws://"):
		return "http://" + strings.TrimPrefix(addr, "ws://")
	case strings.HasPrefix(addr, "wss://"):
		return "https://" + strings.TrimPrefix(addr, "wss://")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr
	}
	// A bare ":8080" is a BIND address meaning every interface; as a destination it means this
	// machine. Printing "http://:8080/#c=…" would hand the user a link no browser can follow.
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// openBrowser asks the desktop to open url. Best-effort: on a headless box there is nothing to open,
// which is exactly the case the printed link exists for.
func openBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}

// errorBody reads a bounded slice of an error response, so a misbehaving endpoint cannot flood stderr.
func errorBody(res *http.Response) string {
	b, _ := io.ReadAll(io.LimitReader(res.Body, 512))
	return strings.TrimSpace(string(b))
}
