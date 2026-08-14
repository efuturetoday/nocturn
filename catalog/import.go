//go:build ignore

// Command import lists the remote MCP servers the official registry knows about, probes each one, and
// writes what it found to mcp/_candidates.json.
//
// It does NOT add anything to the catalog. That is the point: the registry is self-published — a name
// there is a claim by whoever published it — while this catalog is what a daemon offers by default,
// under our name, to somebody who configured nothing. So the machine does the mechanical half (find
// the remotes, check they answer, see whether they want OAuth) and a person moves an entry into
// mcp/<name>.json with a title, tags and a homepage they wrote.
//
// Run it from this directory:
//
//	go run import.go                    list every remote server the registry knows, unprobed
//	go run import.go notion stripe      list those matching, and probe each one
//
// Probing is opt-in per substring because the registry holds thousands of entries and most of them
// are nobody's idea of a curated shelf: an unfiltered probe run is an hour of requests to strangers
// to learn something about servers nobody was going to publish. Leading underscore on the output,
// because generate.go skips those — a candidates file can never be published by accident.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

const (
	registryURL = "https://registry.modelcontextprotocol.io/v0/servers"
	out         = "mcp/_candidates.json"
	pageLimit   = 100
	// maxPages bounds the walk. It has to be well above the registry's real size — the first version
	// stopped at 50 pages, which looked like a complete run and was in fact the letter "a": every
	// vendor anybody would actually want was published under com.* and never seen.
	maxPages   = 1000
	probeEvery = 200 * time.Millisecond
)

// registryPage is the slice of the registry's response this cares about.
type registryPage struct {
	Servers []struct {
		Server struct {
			Name        string `json:"name"`
			Title       string `json:"title"`
			Description string `json:"description"`
			Version     string `json:"version"`
			Repository  struct {
				URL string `json:"url"`
			} `json:"repository"`
			Remotes []struct {
				Type string `json:"type"`
				URL  string `json:"url"`
			} `json:"remotes"`
		} `json:"server"`
		Meta struct {
			Official struct {
				Status   string `json:"status"`
				IsLatest bool   `json:"isLatest"`
			} `json:"io.modelcontextprotocol.registry/official"`
		} `json:"_meta"`
	} `json:"servers"`
	Metadata struct {
		NextCursor string `json:"nextCursor"`
	} `json:"metadata"`
}

// candidate is one probed remote server, written for a human to read.
type candidate struct {
	Suggested   string `json:"suggested"` // a name that would pass discovery.ValidName
	Registry    string `json:"registry"`  // the registry's own name for it
	Title       string `json:"title"`
	Description string `json:"description"`
	Homepage    string `json:"homepage,omitempty"`
	URL         string `json:"url"`
	Auth        string `json:"auth"`   // "" (open or unprobed) or "oauth"
	Status      string `json:"status"` // what the probe saw, empty when this run did not probe
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "import:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	client := &http.Client{Timeout: 20 * time.Second}
	remotes, err := listRemotes(ctx, client)
	if err != nil {
		return err
	}
	fmt.Printf("registry: %d servers with an https remote\n", len(remotes))

	found := filter(remotes, os.Args[1:])
	if len(os.Args) > 1 {
		fmt.Printf("matching %v: %d\n", os.Args[1:], len(found))
		for i := range found {
			found[i].Auth, found[i].Status = probe(ctx, client, found[i].URL)
			fmt.Printf("  [%3d/%3d] %-40s %s\n", i+1, len(found), found[i].Suggested, found[i].Status)
			time.Sleep(probeEvery)
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].Suggested < found[j].Suggested })

	data, err := json.MarshalIndent(found, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(out, append(data, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("import: %d written → %s (nothing is published until you move one into mcp/)\n", len(found), out)
	return nil
}

// filter keeps the candidates matching any of the substrings, across the registry name, the URL and
// the title. No substrings keeps everything — the plain listing run.
func filter(all []candidate, substrings []string) []candidate {
	if len(substrings) == 0 {
		return all
	}
	var out []candidate
	for _, c := range all {
		hay := strings.ToLower(c.Registry + " " + c.URL + " " + c.Title)
		for _, s := range substrings {
			if strings.Contains(hay, strings.ToLower(s)) {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

// listRemotes walks the registry and keeps the latest, active entry per name that offers an https
// streamable-http remote. Everything else this daemon cannot talk to: there is no stdio transport,
// and a plaintext URL is refused by mcp.Server.Validate.
func listRemotes(ctx context.Context, client *http.Client) ([]candidate, error) {
	seen := map[string]bool{}
	var out []candidate
	cursor := ""
	for range maxPages {
		u := fmt.Sprintf("%s?limit=%d", registryURL, pageLimit)
		if cursor != "" {
			u += "&cursor=" + url.QueryEscape(cursor)
		}
		var got registryPage
		if err := getJSON(ctx, client, u, &got); err != nil {
			return nil, err
		}
		for _, s := range got.Servers {
			if !s.Meta.Official.IsLatest || s.Meta.Official.Status != "active" || seen[s.Server.Name] {
				continue
			}
			for _, r := range s.Server.Remotes {
				if r.Type != "streamable-http" || !strings.HasPrefix(r.URL, "https://") {
					continue
				}
				seen[s.Server.Name] = true
				out = append(out, candidate{
					Suggested:   suggestName(s.Server.Name),
					Registry:    s.Server.Name,
					Title:       s.Server.Title,
					Description: s.Server.Description,
					Homepage:    s.Server.Repository.URL,
					URL:         r.URL,
				})
				break // one remote per server; a second URL is the same server again
			}
		}
		if got.Metadata.NextCursor == "" {
			break
		}
		cursor = got.Metadata.NextCursor
	}
	return out, nil
}

// suggestName turns a registry name like "com.example/weather-mcp" into something that could pass
// discovery.ValidName. A suggestion only — the folder name prefixes every tool the server exposes, so
// a person picks the final one.
func suggestName(registry string) string {
	name := registry
	if _, after, ok := strings.Cut(registry, "/"); ok {
		name = after
	}
	name = strings.ToLower(name)
	name = strings.TrimSuffix(strings.TrimSuffix(name, "-mcp"), "-server")
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// probe asks a server to initialize, unauthenticated. An open server answers; one wanting OAuth
// answers 401 with a resource_metadata pointer, which is exactly what auth:"oauth" (spec discovery)
// needs to work. A 401 WITHOUT that pointer is not usable by this client and is reported as such.
func probe(ctx context.Context, client *http.Client, target string) (auth, status string) {
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18",` +
		`"capabilities":{},"clientInfo":{"name":"nocturn-catalog-probe","version":"0"}}}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(body))
	if err != nil {
		return "", "bad url"
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := client.Do(req)
	if err != nil {
		return "", "unreachable"
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))

	switch {
	case resp.StatusCode == http.StatusOK:
		return "", "open"
	case resp.StatusCode == http.StatusUnauthorized &&
		strings.Contains(resp.Header.Get("WWW-Authenticate"), "resource_metadata"):
		return "oauth", "oauth"
	case resp.StatusCode == http.StatusUnauthorized:
		return "", "401 without a resource_metadata pointer"
	default:
		return "", fmt.Sprintf("%s", resp.Status)
	}
}

func getJSON(ctx context.Context, client *http.Client, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", u, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	return json.NewDecoder(bytes.NewReader(data)).Decode(v)
}
