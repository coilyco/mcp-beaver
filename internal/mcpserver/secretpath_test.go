package mcpserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The Telegram shape from mcp-beaver#93: the token IS the base-url path, so a
// failure that reports the URL it failed on emits the credential verbatim.
const fakeBotToken = "bot123456789:AAF-not-a-real-token-0000000000000000000"

func telegramSpec(baseURL string) string {
	return `wrap ward mcp telegram {
    base-url "` + baseURL + `/` + fakeBotToken + `"
    auth none
    can create message {
        path "/sendMessage"
        body { field "text" type="string" }
    }
}`
}

// A failing call must not carry the token, in the log line or in the error the
// caller receives. The failure path is the leak, so it is exercised against a
// throwaway token exactly as the issue asks.
func TestFailedCallLeaksNoBaseURLPathCredential(t *testing.T) {
	resetSecretPaths()
	t.Cleanup(resetSecretPaths)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "upstream is unhappy", http.StatusBadGateway)
	}))
	defer upstream.Close()

	var logs strings.Builder
	SetLogOutput(&logs, "debug")
	t.Cleanup(func() { SetLogOutput(discardWriter{}, "error") })

	s, err := New("telegram", "telegram.mcp.kdl", []byte(telegramSpec(upstream.URL)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp := postAPI(t, ts.Client(), ts.URL+"/api/create_message", "application/json", `{"text":"hi"}`)
	result := decodeAPIResult(t, resp)
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if strings.Contains(string(encoded), fakeBotToken) {
		t.Errorf("the caller's error string carries the token: %s", encoded)
	}
	if strings.Contains(logs.String(), fakeBotToken) {
		t.Errorf("the log line carries the token: %s", logs.String())
	}
	// Decoded, because the marshalled form escapes the angle brackets.
	var decoded struct {
		Content []struct{ Text string } `json:"content"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if len(decoded.Content) == 0 || !strings.Contains(decoded.Content[0].Text, "<redacted>") {
		t.Errorf("the error should say a segment was redacted, got: %s", encoded)
	}
}

// A succeeding call must not reopen it either. Nothing logs a request URL on
// success today, and this is what notices when something starts to.
func TestSucceedingCallLeaksNoBaseURLPathCredential(t *testing.T) {
	resetSecretPaths()
	t.Cleanup(resetSecretPaths)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	var logs strings.Builder
	SetLogOutput(&logs, "debug")
	t.Cleanup(func() { SetLogOutput(discardWriter{}, "error") })

	s, err := New("telegram", "telegram.mcp.kdl", []byte(telegramSpec(upstream.URL)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp := postAPI(t, ts.Client(), ts.URL+"/api/create_message", "application/json", `{"text":"hi"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if strings.Contains(logs.String(), fakeBotToken) {
		t.Errorf("the success log carries the token: %s", logs.String())
	}
}

// The registry masks the whole operator-supplied prefix rather than guessing a
// span inside it, and leaves an ordinary path alone.
func TestRedactSecretPaths(t *testing.T) {
	resetSecretPaths()
	t.Cleanup(resetSecretPaths)

	registerSecretPath("/bot123:secret")
	cases := map[string]string{
		"https://api.telegram.org/bot123:secret/sendMessage": "https://api.telegram.org/<redacted>/sendMessage",
		"https://example.com/v1/things":                      "https://example.com/v1/things",
	}
	for in, want := range cases {
		if got := redactSecretPaths(in); got != want {
			t.Errorf("redactSecretPaths(%q) = %q, want %q", in, got, want)
		}
	}

	// A root path registers nothing: masking "/" would redact every URL.
	resetSecretPaths()
	registerSecretPath("/")
	registerSecretPath("")
	if got := redactSecretPaths("https://example.com/v1"); got != "https://example.com/v1" {
		t.Errorf("a root base-url path must register nothing, got %q", got)
	}
}

// toolError redacts what it hands back, so the caller's copy of a reason is not
// the unredacted twin of the log line.
func TestToolErrorRedacts(t *testing.T) {
	resetSecretPaths()
	t.Cleanup(resetSecretPaths)
	registerSecretPath("/bot123:secret")
	out := toolError(errors.New("GET https://api.telegram.org/bot123:secret/sendMessage -> 502"))
	text := firstTextContent(out)
	if strings.Contains(text, "bot123:secret") {
		t.Errorf("toolError handed the caller a credential: %q", text)
	}
	if !strings.Contains(text, "<redacted>") {
		t.Errorf("want a redaction marker, got %q", text)
	}
	_ = fmt.Sprint(out.IsError)
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
