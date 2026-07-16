package script_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/efuturetoday/nocturn/internal/script"
	"github.com/efuturetoday/nocturn/internal/tool"
)

func fakeTool(name string, fn func(args string) (string, error)) tool.Tool {
	return tool.Tool{
		Spec:   tool.Spec{Name: name},
		Invoke: func(_ context.Context, args string) (string, error) { return fn(args) },
	}
}

func runScript(t *testing.T, tools []tool.Tool, src string) string {
	t.Helper()
	r := script.New(tool.NewRegistry(tools))
	out, err := r.Run(context.Background(), src)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return strings.TrimSpace(out)
}

// The pure-compute polyfills work on the real QuickJS guest: base64, UTF-8
// encode/decode, URL parsing, URLSearchParams, and Buffer base64url.
func TestPrelude_ComputePolyfills(t *testing.T) {
	out := runScript(t, nil, `
		const enc = new TextEncoder().encode("héllo");
		const u = new URL("https://api.example.com:8443/p/q?x=1&y=2#h");
		console.log(JSON.stringify({
			b64: btoa("hi"),
			round: atob(btoa("hello world")),
			dec: new TextDecoder().decode(enc),
			encLen: enc.length,
			host: u.hostname, port: u.port, path: u.pathname, x: u.searchParams.get("x"),
			qs: new URLSearchParams({a:"1 2", b:"3"}).toString(),
			bufB64url: Buffer.from("subject","utf8").toString("base64url"),
		}));
	`)
	var got struct {
		B64, Round, Dec, Host, Port, Path, X, QS, BufB64url string
		EncLen                                              int
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output %q: %v", out, err)
	}
	want := map[string]string{
		"b64":       "aGk=",
		"round":     "hello world",
		"dec":       "héllo",
		"host":      "api.example.com",
		"port":      "8443",
		"path":      "/p/q",
		"x":         "1",
		"qs":        "a=1+2&b=3",
		"bufB64url": "c3ViamVjdA",
	}
	for k, w := range want {
		var g string
		switch k {
		case "b64":
			g = got.B64
		case "round":
			g = got.Round
		case "dec":
			g = got.Dec
		case "host":
			g = got.Host
		case "port":
			g = got.Port
		case "path":
			g = got.Path
		case "x":
			g = got.X
		case "qs":
			g = got.QS
		case "bufB64url":
			g = got.BufB64url
		}
		if g != w {
			t.Errorf("%s = %q, want %q", k, g, w)
		}
	}
	if got.EncLen != 6 { // "héllo": é is 2 UTF-8 bytes
		t.Errorf("encLen = %d, want 6", got.EncLen)
	}
}

// fetch() routes a GET through http.read and exposes an honest Response.
func TestPrelude_FetchRead(t *testing.T) {
	tools := []tool.Tool{fakeTool("http.read", func(string) (string, error) {
		return `{"status":200,"statusText":"OK","headers":{"Content-Type":"text/plain"},"body":"hello"}`, nil
	})}
	out := runScript(t, tools, `
		const r = await fetch("https://x.example/y");
		console.log(r.status + " " + r.ok + " " + r.headers.get("content-type") + " " + (await r.text()));
	`)
	if out != "200 true text/plain hello" {
		t.Fatalf("out = %q", out)
	}
}

// fetch() with a URLSearchParams body serializes to urlencoded through http.write.
func TestPrelude_FetchWriteUrlencoded(t *testing.T) {
	tools := []tool.Tool{fakeTool("http.write", func(args string) (string, error) {
		var a struct {
			Body        string `json:"body"`
			ContentType string `json:"content_type"`
		}
		_ = json.Unmarshal([]byte(args), &a)
		env, _ := json.Marshal(map[string]any{"status": 200, "body": a.ContentType + "|" + a.Body})
		return string(env), nil
	})}
	out := runScript(t, tools, `
		const r = await fetch("https://x/y", {method:"POST", body: new URLSearchParams({a:"1",b:"2 3"})});
		console.log(await r.text());
	`)
	if out != "application/x-www-form-urlencoded;charset=UTF-8|a=1&b=2+3" {
		t.Fatalf("out = %q", out)
	}
}

// fetch() with a FormData body serializes to multipart through http.write.
func TestPrelude_FetchWriteMultipart(t *testing.T) {
	tools := []tool.Tool{fakeTool("http.write", func(args string) (string, error) {
		var a struct {
			Body        string `json:"body"`
			ContentType string `json:"content_type"`
		}
		_ = json.Unmarshal([]byte(args), &a)
		env, _ := json.Marshal(map[string]any{"status": 200, "body": a.ContentType + "\n" + a.Body})
		return string(env), nil
	})}
	out := runScript(t, tools, `
		const fd = new FormData();
		fd.append("field", "value");
		const r = await fetch("https://x/y", {method:"POST", body: fd});
		console.log(await r.text());
	`)
	if !strings.HasPrefix(out, "multipart/form-data; boundary=----NocturnFormBoundary") {
		t.Fatalf("content type wrong: %q", out)
	}
	if !strings.Contains(out, `Content-Disposition: form-data; name="field"`) || !strings.Contains(out, "value") {
		t.Fatalf("multipart body missing field: %q", out)
	}
}

// fs (sync) and nocturn.fs (async) both route through the file.* tools.
func TestPrelude_FsThroughTools(t *testing.T) {
	files := map[string]string{}
	tools := []tool.Tool{
		fakeTool("file.write", func(args string) (string, error) {
			var a struct{ Path, Content string }
			_ = json.Unmarshal([]byte(args), &a)
			files[a.Path] = a.Content
			return "wrote", nil
		}),
		fakeTool("file.read", func(args string) (string, error) {
			var a struct{ Path string }
			_ = json.Unmarshal([]byte(args), &a)
			return files[a.Path], nil
		}),
	}
	out := runScript(t, tools, `
		fs.writeFileSync("a.txt", "data");
		await nocturn.fs.writeFile("b.txt", "async");
		console.log(fs.readFileSync("a.txt") + "|" + (await nocturn.fs.readFile("b.txt")));
	`)
	if out != "data|async" {
		t.Fatalf("out = %q", out)
	}
}

// A denied/failed effect (the gate throws) surfaces as a rejected fetch Promise —
// matching WHATWG fetch's network-error rejection — and is catchable.
func TestPrelude_FetchRejectsOnError(t *testing.T) {
	tools := []tool.Tool{fakeTool("http.read", func(string) (string, error) {
		return "", context.DeadlineExceeded
	})}
	out := runScript(t, tools, `
		try { await fetch("https://x/y"); console.log("REACHED"); }
		catch (e) { console.log("caught"); }
	`)
	if out != "caught" {
		t.Fatalf("out = %q, want caught", out)
	}
}
