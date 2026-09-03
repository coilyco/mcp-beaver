package teableadmin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A pagination bug that never returns a short page must not spin forever: it
// has to fail loud within a bound instead.
func TestSnapshotValuesFailsClosedRatherThanLoopingForever(t *testing.T) {
	t.Parallel()
	fake := newFakeTeable()
	fake.mux.HandleFunc("/table/tbl1/record", func(w http.ResponseWriter, r *http.Request) {
		records := make([]struct {
			ID     string         `json:"id"`
			Fields map[string]any `json:"fields"`
		}, pageSize)
		writeJSON(w, recordPage{Records: records})
	})
	srv := fake.server()
	defer srv.Close()
	client := &Client{BaseURL: srv.URL, Token: "t"}

	_, err := client.SnapshotValues(context.Background(), "tbl1", "fld1")
	if err == nil {
		t.Fatal("an unending full page did not fail")
	}
	if !strings.Contains(err.Error(), "did not terminate") {
		t.Errorf("error = %q, want it to name the pagination bound", err)
	}
}

// A non-2xx response is surfaced with the status and body, not swallowed
// into a generic failure a caller cannot act on.
func TestDoSurfacesTheUpstreamStatusAndBody(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Unauthorized","code":"unauthorized"}`))
	}))
	defer srv.Close()
	client := &Client{BaseURL: srv.URL, Token: "t"}

	_, err := client.ListFields(context.Background(), "tbl1")
	if err == nil {
		t.Fatal("a 401 was not reported as an error")
	}
	if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "Unauthorized") {
		t.Errorf("error = %q, want the status and body", err)
	}
}

// The bearer token reaches the request header and never the URL, so it
// cannot land in a proxy access log or a browser history.
func TestTokenIsSentAsABearerHeaderNotAQueryParameter(t *testing.T) {
	t.Parallel()
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		writeJSON(w, []Field{})
	}))
	defer srv.Close()
	client := &Client{BaseURL: srv.URL, Token: "secret-token"}

	if _, err := client.ListFields(context.Background(), "tbl1"); err != nil {
		t.Fatalf("ListFields: %v", err)
	}
	if gotAuth != "Bearer secret-token" {
		t.Errorf("Authorization header = %q", gotAuth)
	}
}
