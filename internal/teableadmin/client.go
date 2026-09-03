// Package teableadmin is a direct HTTP client for the parts of Teable's API a
// schema change needs. It never goes through a guarded MCP surface: the whole
// point of teable-admin is the multi-step read-verify-restore logic no
// guardfile's single-call allowlist can express, run by an operator who has
// decided to make a schema change rather than by an agent turn.
//
// Every write here is followed by an independent read that does not trust the
// write's own response body, because Teable's own defects are exactly a write
// that reports success without doing the thing: unknown field properties
// silently discarded, an edit that returns 200 and applies nothing, and a
// convert that once emptied 6,536 values while reporting the opposite.
package teableadmin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Client talks to one Teable base over its REST API.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// pageSize bounds one paginated record read. Teable returns no total count in
// any response this tool has observed, so the end of a table is detected by a
// short page rather than counted toward up front.
const pageSize = 200

// maxPages bounds a value snapshot so a pagination bug fails loud rather than
// looping until the process is killed. 100 pages at pageSize is 20,000 rows.
const maxPages = 100

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// do issues one request and decodes a JSON response into out, which may be
// nil for a call whose body is not needed. A non-2xx status is returned as an
// error carrying the status and body, so a caller sees exactly what Teable
// said rather than a generic failure.
func (c *Client) do(
	ctx context.Context, method, path string, query url.Values, body any, out any,
) error {
	endpoint := strings.TrimRight(c.BaseURL, "/") + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request body: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("%s %s: read response: %w", method, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s -> %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s %s: decode response: %w", method, path, err)
	}
	return nil
}

// Field is a Teable field object, kept as a raw map rather than a typed
// struct. Diffing is done key-by-key against a caller-supplied request body,
// and a struct would have to track Teable's schema to stay lossless.
type Field map[string]any

// ID reads the field's own id, empty if absent.
func (f Field) ID() string {
	id, _ := f["id"].(string)
	return id
}

// ListFields reads every field on a table. The only field read this client
// has: there is no documented single-field GET, so a read-back after a write
// finds its target in this list by id.
func (c *Client) ListFields(ctx context.Context, tableID string) ([]Field, error) {
	var fields []Field
	if err := c.do(ctx, http.MethodGet, "/table/"+tableID+"/field", nil, nil, &fields); err != nil {
		return nil, err
	}
	return fields, nil
}

// FieldByID re-reads the field list and returns the one matching id. Always a
// fresh call: never reuses a list a caller already has, because a stale list
// is exactly what would hide a write that did not apply.
func (c *Client) FieldByID(ctx context.Context, tableID, fieldID string) (Field, bool, error) {
	fields, err := c.ListFields(ctx, tableID)
	if err != nil {
		return nil, false, err
	}
	for _, f := range fields {
		if f.ID() == fieldID {
			return f, true, nil
		}
	}
	return nil, false, nil
}

// CreateField posts a new field. The response is read but never trusted on
// its own: the caller re-reads through FieldByID before deciding success.
func (c *Client) CreateField(ctx context.Context, tableID string, spec map[string]any) (Field, error) {
	var created Field
	if err := c.do(ctx, http.MethodPost, "/table/"+tableID+"/field", nil, spec, &created); err != nil {
		return nil, err
	}
	return created, nil
}

// EditField PATCHes an existing field's definition. Teable's own defect here
// is that this can return 200, validate nothing, and apply nothing, so the
// caller re-reads through FieldByID rather than trusting this call returned.
func (c *Client) EditField(ctx context.Context, tableID, fieldID string, patch map[string]any) error {
	return c.do(ctx, http.MethodPatch, "/table/"+tableID+"/field/"+fieldID, nil, patch, nil)
}

// ConvertField changes a field's type, the one verb documented to have
// destroyed stored values while reporting success. The caller snapshots
// every row's value before calling this and diffs after, because nothing
// about this response can be trusted to say what happened to existing data.
func (c *Client) ConvertField(ctx context.Context, tableID, fieldID string, spec map[string]any) error {
	return c.do(ctx, http.MethodPut, "/table/"+tableID+"/field/"+fieldID+"/convert", nil, spec, nil)
}

// recordPage is the shape list_record returns.
type recordPage struct {
	Records []struct {
		ID     string         `json:"id"`
		Fields map[string]any `json:"fields"`
	} `json:"records"`
}

// SnapshotValues reads every row's value for one field, keyed by record id.
// fieldKeyType is "id" rather than "name" throughout this package: an edit
// can rename the field mid-operation, and only the id stays valid across
// that rename for the after-snapshot to line up against the before one.
func (c *Client) SnapshotValues(ctx context.Context, tableID, fieldID string) (map[string]any, error) {
	values := make(map[string]any)
	for page := 0; page < maxPages; page++ {
		query := url.Values{
			"fieldKeyType": {"id"},
			"projection":   {fieldID},
			"take":         {fmt.Sprint(pageSize)},
			"skip":         {fmt.Sprint(page * pageSize)},
		}
		var body recordPage
		if err := c.do(ctx, http.MethodGet, "/table/"+tableID+"/record", query, nil, &body); err != nil {
			return nil, fmt.Errorf("snapshot page %d: %w", page, err)
		}
		for _, record := range body.Records {
			values[record.ID] = record.Fields[fieldID]
		}
		if len(body.Records) < pageSize {
			return values, nil
		}
	}
	return nil, fmt.Errorf(
		"snapshot did not terminate within %d pages (%d rows): pagination is likely broken, refusing to guess the table ended",
		maxPages, maxPages*pageSize,
	)
}

// RestoreValue writes one field's value back onto one record, used to undo
// data a convert destroyed. fieldKeyType "id" matches SnapshotValues.
func (c *Client) RestoreValue(ctx context.Context, tableID, recordID, fieldID string, value any) error {
	body := map[string]any{
		"fieldKeyType": "id",
		"typecast":     true,
		"record": map[string]any{
			"fields": map[string]any{fieldID: value},
		},
	}
	return c.do(ctx, http.MethodPatch, "/table/"+tableID+"/record/"+recordID, nil, body, nil)
}
