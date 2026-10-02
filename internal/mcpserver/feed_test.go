package mcpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coilyco/umbra/http/opcore"
)

// atomFeed writes the Atom shape reddit serves: entries carrying a title, an
// `<author><name>`, a `rel="alternate"` link, a category, and both timestamps.
func buildAtomFeed(titles ...string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<feed xmlns="http://www.w3.org/2005/Atom">` + "\n")
	b.WriteString(`<title>newest submissions</title>` + "\n")
	for i, title := range titles {
		fmt.Fprintf(&b, `<entry>
  <category term="golang" label="r/golang"/>
  <author><name>/u/someone</name></author>
  <link rel="alternate" href="https://example.invalid/post/%d"/>
  <link rel="self" href="https://example.invalid/self/%d"/>
  <updated>2026-08-1%dT00:00:00+00:00</updated>
  <published>2026-08-0%dT00:00:00+00:00</published>
  <title>%s</title>
  <id>tag:example.invalid,2026:post/%d</id>
  <content type="html">&lt;p&gt;a very large post body nobody needs&lt;/p&gt;</content>
</entry>
`, i, i, i%10, i%10, title, i)
	}
	b.WriteString("</feed>\n")
	return b.String()
}

// rss2Feed writes the other shape a syndicated source serves: items under a
// channel, a text `<link>`, a `<dc:creator>`, a `<guid>`, and a `<pubDate>`.
func buildRSS2Feed(titles ...string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<rss version="2.0" xmlns:dc="http://purl.org/dc/elements/1.1/"><channel>` + "\n")
	b.WriteString(`<title>a plain rss channel</title>` + "\n")
	for i, title := range titles {
		fmt.Fprintf(&b, `<item>
  <title>%s</title>
  <link>https://example.invalid/rss/%d</link>
  <dc:creator>someone else</dc:creator>
  <guid isPermaLink="false">rss-guid-%d</guid>
  <pubDate>Mon, 03 Aug 2026 00:00:00 +0000</pubDate>
  <category>announcements</category>
  <description>a very large description nobody needs</description>
</item>
`, title, i, i)
	}
	b.WriteString("</channel></rss>\n")
	return b.String()
}

func feedExtractSpec(baseURL, siblings string) string {
	return siblings + `
wrap ward mcp news {
    base-url "` + baseURL + `"
    auth none
    can get feed {
        path "/feed.xml"
        raw-response
    }
}`
}

func serveFeed(t *testing.T, body string, siblings string) map[string]any {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/atom+xml")
		_, _ = w.Write([]byte(body))
	}))
	defer upstream.Close()

	s, err := New("news", "news.mcp.kdl", []byte(feedExtractSpec(upstream.URL, siblings)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	initResp := postToServer(t, ts.Client(), ts.URL+"/mcp", "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"mcp-beaver-test","version":"0.1.0"}}}`)
	return callTool(t, ts, initResp.Header.Get("Mcp-Session-Id"), "2", "get_feed", `{}`)
}

// feedPayload is the projected envelope as a caller reads it off the wire.
type feedPayload struct {
	Coverage coverage   `json:"coverage"`
	Result   feedResult `json:"result"`
}

func decodeFeedPayload(t *testing.T, result map[string]any) feedPayload {
	t.Helper()
	if isErr, _ := result["isError"].(bool); isErr {
		t.Fatalf("extraction reported isError; content=%v", result["content"])
	}
	var payload feedPayload
	if err := json.Unmarshal([]byte(firstText(t, result)), &payload); err != nil {
		t.Fatalf("envelope: %v", err)
	}
	return payload
}

// The ask: a feed reachable from a granted upstream becomes entries an agent
// can read. Without this it returns XML the model parses in context on every
// call, which is the cost examples/reddit.mcp.kdl states in its own header.
func TestExtractTurnsAnAtomFeedIntoEntries(t *testing.T) {
	payload := decodeFeedPayload(t, serveFeed(t, buildAtomFeed("first post", "second post"), `extract "get_feed" as="feed-entries"`))

	if payload.Result.Title != "newest submissions" {
		t.Errorf("feed title = %q, want the channel title", payload.Result.Title)
	}
	if len(payload.Result.Entries) != 2 {
		t.Fatalf("entries = %d, want 2: %+v", len(payload.Result.Entries), payload.Result.Entries)
	}
	got := payload.Result.Entries[0]
	if got.Title != "first post" {
		t.Errorf("title = %q", got.Title)
	}
	// rel="self" is present and must lose to rel="alternate": a model handed
	// the self link would fetch the entry's own feed record rather than the post.
	if got.Link != "https://example.invalid/post/0" {
		t.Errorf("link = %q, want the alternate link rather than the self link", got.Link)
	}
	if got.Author != "/u/someone" {
		t.Errorf("author = %q, want the <author><name>", got.Author)
	}
	if got.ID != "tag:example.invalid,2026:post/0" {
		t.Errorf("id = %q", got.ID)
	}
	if got.Published == "" || got.Updated == "" {
		t.Errorf("timestamps = %q / %q, want both", got.Published, got.Updated)
	}
	// Reddit carries the subreddit as a category term, so this is the field a
	// source-specific label reaches the model through.
	if len(got.Categories) != 1 || got.Categories[0] != "golang" {
		t.Errorf("categories = %v, want the category term", got.Categories)
	}
}

// RSS 2.0 is the other shape a syndicated source serves, and it spells every
// field differently. One `as` value has to cover both or a guardfile author
// has to know which dialect their upstream picked.
func TestExtractTurnsAnRSSFeedIntoEntries(t *testing.T) {
	payload := decodeFeedPayload(t, serveFeed(t, buildRSS2Feed("rss post"), `extract "get_feed" as="feed-entries"`))

	if payload.Result.Title != "a plain rss channel" {
		t.Errorf("feed title = %q, want the channel title", payload.Result.Title)
	}
	if len(payload.Result.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(payload.Result.Entries))
	}
	got := payload.Result.Entries[0]
	if got.Title != "rss post" {
		t.Errorf("title = %q", got.Title)
	}
	if got.Link != "https://example.invalid/rss/0" {
		t.Errorf("link = %q, want the text <link>", got.Link)
	}
	if got.Author != "someone else" {
		t.Errorf("author = %q, want the dc:creator", got.Author)
	}
	if got.ID != "rss-guid-0" {
		t.Errorf("id = %q, want the guid", got.ID)
	}
	if got.Published != "Mon, 03 Aug 2026 00:00:00 +0000" {
		t.Errorf("published = %q, want the pubDate", got.Published)
	}
	if len(got.Categories) != 1 || got.Categories[0] != "announcements" {
		t.Errorf("categories = %v, want the category text", got.Categories)
	}
}

// The projection exists to remove bytes, so the entry body must not come with
// it. Reddit's per-entry content is the whole post as HTML, and carrying it
// would return most of what the projection is for.
func TestExtractLeavesTheEntryBodyBehind(t *testing.T) {
	payload := decodeFeedPayload(t, serveFeed(t, buildAtomFeed("first post"), `extract "get_feed" as="feed-entries"`))
	encoded, err := json.Marshal(payload.Result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), "nobody needs") {
		t.Errorf("projection carried the entry body: %s", encoded)
	}
}

// A bounded read that looks complete is the failure #68 is about, arriving
// through a third door. The model has to be able to tell entry 4 exists.
func TestExtractBoundsEntriesAndSaysSo(t *testing.T) {
	titles := make([]string, 0, 12)
	for i := range 12 {
		titles = append(titles, fmt.Sprintf("post %d", i+1))
	}
	payload := decodeFeedPayload(t, serveFeed(t, buildAtomFeed(titles...), `extract "get_feed" as="feed-entries" max-items="3"`))

	if payload.Coverage.Entries == nil {
		t.Fatalf("no entry coverage: %+v", payload.Coverage)
	}
	if got := *payload.Coverage.Entries; got.Shown != 3 || got.Total != 12 {
		t.Fatalf("entries = %+v, want 3 of 12", got)
	}
	if len(payload.Result.Entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(payload.Result.Entries))
	}
	if payload.Result.Entries[2].Title != "post 3" {
		t.Errorf("last entry = %q, want the third", payload.Result.Entries[2].Title)
	}
}

// A default the guardfile raises rather than a ceiling it lowers into, the
// same posture max-pages takes.
func TestExtractDefaultsTheEntryBound(t *testing.T) {
	titles := make([]string, 0, defaultFeedItems+5)
	for i := range defaultFeedItems + 5 {
		titles = append(titles, fmt.Sprintf("post %d", i+1))
	}
	payload := decodeFeedPayload(t, serveFeed(t, buildAtomFeed(titles...), `extract "get_feed" as="feed-entries"`))
	if got := *payload.Coverage.Entries; got.Shown != defaultFeedItems || got.Total != defaultFeedItems+5 {
		t.Fatalf("entries = %+v, want %d of %d", got, defaultFeedItems, defaultFeedItems+5)
	}
}

// A response that is not a feed is a clean tool error naming the document,
// rather than an empty entry list a model would read as "nothing was posted".
func TestExtractFailsCleanlyOnANonFeed(t *testing.T) {
	for name, body := range map[string]string{
		"malformed xml": `<feed><entry><title>unclosed`,
		"not a feed":    `<?xml version="1.0"?><status><code>200</code></status>`,
		"html":          `<!doctype html><html><body>a web page</body></html>`,
	} {
		t.Run(name, func(t *testing.T) {
			result := serveFeed(t, body, `extract "get_feed" as="feed-entries"`)
			if isErr, _ := result["isError"].(bool); !isErr {
				t.Fatalf("a non-feed returned success: %v", result)
			}
			if got := firstText(t, result); !strings.Contains(got, "feed") {
				t.Errorf("error = %q, want it to name the feed", got)
			}
		})
	}
}

// An empty feed is a real answer and must not read as a failure: a subreddit
// with no new posts is a fact rather than an error.
func TestExtractServesAnEmptyFeed(t *testing.T) {
	payload := decodeFeedPayload(t, serveFeed(t, buildAtomFeed(), `extract "get_feed" as="feed-entries"`))
	if payload.Result.Entries == nil {
		t.Fatalf("entries serialized as null rather than an empty list")
	}
	if len(payload.Result.Entries) != 0 {
		t.Fatalf("entries = %d, want 0", len(payload.Result.Entries))
	}
	if got := *payload.Coverage.Entries; got.Shown != 0 || got.Total != 0 {
		t.Errorf("entries = %+v, want 0 of 0", got)
	}
}

// The build-time half. An unknown `as` value and a bound that belongs to the
// other kind both stay build errors, so a spec that cannot do what its author
// believes never starts.
func TestFeedExtractFailsClosedAtBuild(t *testing.T) {
	for name, tc := range map[string]struct {
		siblings string
		want     string
	}{
		"pages on a feed": {`extract "get_feed" as="feed-entries" max-pages="5"`, "bounds items rather than pages"},
		"bad max-items":   {`extract "get_feed" as="feed-entries" max-items="lots"`, "must be a whole number"},
		"zero max-items":  {`extract "get_feed" as="feed-entries" max-items="0"`, "must be between 1 and 250"},
		"over ceiling":    {`extract "get_feed" as="feed-entries" max-items="5000"`, "must be between 1 and 250"},
		"unknown kind":    {`extract "get_feed" as="feed-items"`, "needs `as=\"pdf-text\"`"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := New("news", "news.mcp.kdl", []byte(feedExtractSpec("http://127.0.0.1:1", tc.siblings)))
			if err == nil {
				t.Fatalf("New accepted %q", tc.siblings)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want it to name %q", err, tc.want)
			}
		})
	}
}

// Without `raw-response` opcore decodes the body as JSON and the call fails on
// the first byte of `<`, long before extraction runs. Unchanged by this kind
// landing, and asserted here because the reason now covers two formats.
func TestFeedExtractRequiresRawResponse(t *testing.T) {
	spec := `extract "get_feed" as="feed-entries"
wrap ward mcp news {
    base-url "http://127.0.0.1:1"
    auth none
    can get feed {
        path "/feed.xml"
    }
}`
	_, err := New("news", "news.mcp.kdl", []byte(spec))
	if err == nil {
		t.Fatalf("New accepted a feed extract without raw-response")
	}
	if !strings.Contains(err.Error(), "raw-response") {
		t.Fatalf("error = %q, want it to name raw-response", err)
	}
}

// The size gate sits before the parser, so an oversize document is refused
// without being parsed at all. Exercised directly rather than over the wire:
// serving 8MB through httptest would test the loopback, not the gate.
func TestFeedExtractRefusesAnOversizeDocument(t *testing.T) {
	oversize := opcore.Response{Raw: make([]byte, maxFeedBytes+1)}
	result := feedToolSuccess(oversize, &extractSpec{kind: kindFeedEntries, maxItems: 5})
	if !result.IsError {
		t.Fatalf("an oversize feed was accepted")
	}
}
