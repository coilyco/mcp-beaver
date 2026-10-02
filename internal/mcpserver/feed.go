package mcpserver

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"strings"

	"github.com/coilyco/umbra/http/opcore"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxFeedBytes bounds the document this runtime will parse at all, for the
// same reason maxPDFBytes does: the size gate comes before the parser rather
// than from it. Feeds are small by convention - reddit serves 53 KB - so this
// is generous rather than tuned, and a source past it is a source that stopped
// being a feed.
const maxFeedBytes = 8 << 20

// defaultFeedItems is the entry bound when a guardfile states none. 25 is what
// reddit's Atom carries per feed, and like defaultExtractPages it is the
// default the guardfile raises rather than a ceiling it lowers into.
const defaultFeedItems = 25

// maxFeedItems ceilings what a guardfile may raise the bound to. Past this a
// feed read is a search that wants a different tool.
const maxFeedItems = 250

// feedEntry is one normalized item, the same shape whether the source spelled
// it Atom or RSS.
//
// The entry body is deliberately absent. The whole reason a guardfile asks for
// this projection is that raw syndication is too large to put in front of a
// model, and reddit's per-entry `content` is the full post as HTML - carrying
// it would return most of the bytes the projection exists to remove. What
// survives is what a model uses to decide which entry to fetch.
type feedEntry struct {
	Title      string   `json:"title,omitempty"`
	Link       string   `json:"link,omitempty"`
	Author     string   `json:"author,omitempty"`
	ID         string   `json:"id,omitempty"`
	Published  string   `json:"published,omitempty"`
	Updated    string   `json:"updated,omitempty"`
	Categories []string `json:"categories,omitempty"`
}

// feedResult is the projected payload. Entries is never omitted: an empty feed
// and a feed this runtime failed to read must not serialize identically.
type feedResult struct {
	Title   string      `json:"title,omitempty"`
	Entries []feedEntry `json:"entries"`
}

// feedDocument reads both syndication shapes from one root. Atom hangs entries
// off the root, RSS hangs items off a channel, and no document carries both -
// so whichever is populated is the shape that arrived.
type feedDocument struct {
	Title   string        `xml:"title"`
	Entries []feedItemXML `xml:"entry"`
	Channel *struct {
		Title string        `xml:"title"`
		Items []feedItemXML `xml:"item"`
	} `xml:"channel"`
}

// feedItemXML is the union of the two spellings. encoding/xml matches on local
// name when the tag names no namespace, so `creator` catches `dc:creator`
// without this having to model namespaces.
type feedItemXML struct {
	Title      string        `xml:"title"`
	Links      []feedLinkXML `xml:"link"`
	Author     feedAuthorXML `xml:"author"`
	Creator    string        `xml:"creator"`
	ID         string        `xml:"id"`
	GUID       string        `xml:"guid"`
	Published  string        `xml:"published"`
	Updated    string        `xml:"updated"`
	PubDate    string        `xml:"pubDate"`
	Date       string        `xml:"date"`
	Categories []feedCatXML  `xml:"category"`
}

// feedLinkXML covers Atom's attribute form and RSS's text form in one type.
type feedLinkXML struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
	Text string `xml:",chardata"`
}

// feedAuthorXML covers Atom's `<author><name>` and RSS's bare text.
type feedAuthorXML struct {
	Name string `xml:"name"`
	Text string `xml:",chardata"`
}

// feedCatXML covers Atom's `term` attribute and RSS's element text. Reddit
// carries the subreddit here, so this is how a source-specific label reaches
// the model without this projection knowing anything about reddit.
type feedCatXML struct {
	Term string `xml:"term,attr"`
	Text string `xml:",chardata"`
}

// feedToolSuccess replaces the syndication XML with entries an agent can read,
// and reports entries shown of entries total the way pdfToolSuccess reports
// pages, so a bounded feed read cannot be mistaken for the whole feed.
func feedToolSuccess(resp opcore.Response, x *extractSpec) *mcp.CallToolResult {
	if len(resp.Raw) > maxFeedBytes {
		return toolError(fmt.Errorf("mcp-beaver: feed is %d bytes, over the %d-byte extraction ceiling", len(resp.Raw), maxFeedBytes))
	}
	result, total, err := parseFeed(resp.Raw, x.maxItems)
	if err != nil {
		return toolError(err)
	}
	// Bytes measures the projected entries rather than the source document: it
	// is what the consumer carries, and it is what its cap applies to.
	encodedResult, err := json.Marshal(result)
	if err != nil {
		return toolError(fmt.Errorf("serialize tool result: %w", err))
	}
	payload := toolPayload{
		Coverage: coverage{
			Truncated:  false,
			Bytes:      len(encodedResult),
			OverBudget: len(encodedResult) > consumerBudgetBytes,
			Entries:    &entryCoverage{Shown: len(result.Entries), Total: total},
		},
		Result: result,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return toolError(fmt.Errorf("serialize tool result: %w", err))
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: string(encoded)}},
		StructuredContent: payload,
	}
}

// parseFeed decodes one feed and returns up to maxItems entries plus the count
// the document held, so the caller can state shown-of-total.
//
// The decoder is given a CharsetReader that passes non-UTF-8 bytes through
// unchanged. Without one, encoding/xml refuses any document declaring a
// charset it does not know, and syndication in the wild still ships ISO-8859-1
// - refusing the whole feed over an accented byte would be a worse answer than
// carrying that byte through.
func parseFeed(raw []byte, maxItems int) (feedResult, int, error) {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	decoder.Strict = false
	decoder.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) { return input, nil }

	var doc feedDocument
	if err := decoder.Decode(&doc); err != nil {
		return feedResult{}, 0, fmt.Errorf("mcp-beaver: cannot read feed: %w", err)
	}

	title, items := doc.Title, doc.Entries
	if doc.Channel != nil {
		title, items = doc.Channel.Title, doc.Channel.Items
	}
	// A well-formed document that is not a feed decodes into an empty struct
	// rather than an error, so the absence of both shapes is where this says
	// so. Naming the document keeps the message actionable for whoever wrote
	// the grant.
	if len(items) == 0 && doc.Channel == nil && len(doc.Entries) == 0 && strings.TrimSpace(title) == "" {
		return feedResult{}, 0, fmt.Errorf("mcp-beaver: response carries no RSS or Atom feed")
	}

	out := feedResult{Title: strings.TrimSpace(title), Entries: []feedEntry{}}
	for _, item := range items {
		if len(out.Entries) >= maxItems {
			break
		}
		out.Entries = append(out.Entries, normalizeEntry(item))
	}
	return out, len(items), nil
}

// normalizeEntry folds one item's two possible spellings into one shape,
// preferring the Atom field and falling back to the RSS one.
func normalizeEntry(item feedItemXML) feedEntry {
	entry := feedEntry{
		Title:     strings.TrimSpace(item.Title),
		Link:      entryLink(item.Links),
		Author:    firstNonEmpty(strings.TrimSpace(item.Author.Name), strings.TrimSpace(item.Author.Text), strings.TrimSpace(item.Creator)),
		ID:        firstNonEmpty(strings.TrimSpace(item.ID), strings.TrimSpace(item.GUID)),
		Published: firstNonEmpty(strings.TrimSpace(item.Published), strings.TrimSpace(item.PubDate), strings.TrimSpace(item.Date)),
		Updated:   strings.TrimSpace(item.Updated),
	}
	for _, cat := range item.Categories {
		if label := firstNonEmpty(strings.TrimSpace(cat.Term), strings.TrimSpace(cat.Text)); label != "" {
			entry.Categories = append(entry.Categories, label)
		}
	}
	return entry
}

// entryLink picks the one link a reader would follow. Atom hangs several off
// an entry and distinguishes them by `rel`, where `alternate` (stated or
// defaulted) is the human-readable one and `enclosure` or `self` are not.
func entryLink(links []feedLinkXML) string {
	for _, link := range links {
		if link.Rel != "" && link.Rel != "alternate" {
			continue
		}
		if href := strings.TrimSpace(link.Href); href != "" {
			return href
		}
		if text := strings.TrimSpace(link.Text); text != "" {
			return text
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
