package teableadmin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeTeable is a minimal in-memory Teable, just enough surface for this
// package's client calls. Handlers are swapped per test so each one models
// exactly the defect it is checking for.
type fakeTeable struct {
	mux *http.ServeMux
}

func newFakeTeable() *fakeTeable {
	return &fakeTeable{mux: http.NewServeMux()}
}

func (f *fakeTeable) server() *httptest.Server { return httptest.NewServer(f.mux) }

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// --- create-field ---------------------------------------------------------

// The reported defect: Teable accepts a field with five properties and
// stores three, returning 200 either way. The read-back is what catches it.
func TestCreateFieldRefusesWhenTeableDropsAProperty(t *testing.T) {
	t.Parallel()
	fake := newFakeTeable()
	stored := Field{"id": "fld1", "name": "priority", "type": "singleSelect"} // "options" requested, never stored
	fake.mux.HandleFunc("/table/tbl1/field", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			writeJSON(w, stored)
		case http.MethodGet:
			writeJSON(w, []Field{stored})
		}
	})
	srv := fake.server()
	defer srv.Close()
	client := &Client{BaseURL: srv.URL, Token: "t"}

	result, err := CreateField(context.Background(), client, "tbl1", map[string]any{
		"name": "priority", "type": "singleSelect", "options": map[string]any{"choices": []any{"P0"}},
	})
	if err != nil {
		t.Fatalf("CreateField: %v", err)
	}
	if result.Success {
		t.Fatal("a dropped property was reported as success")
	}
	if len(result.Mismatches) != 1 || !strings.Contains(result.Mismatches[0], "options") {
		t.Errorf("mismatches = %v, want exactly the dropped options key", result.Mismatches)
	}
}

func TestCreateFieldSucceedsWhenEveryPropertySurvives(t *testing.T) {
	t.Parallel()
	fake := newFakeTeable()
	stored := Field{"id": "fld1", "name": "priority", "type": "singleSelect"}
	fake.mux.HandleFunc("/table/tbl1/field", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			writeJSON(w, stored)
		case http.MethodGet:
			writeJSON(w, []Field{stored})
		}
	})
	srv := fake.server()
	defer srv.Close()
	client := &Client{BaseURL: srv.URL, Token: "t"}

	result, err := CreateField(context.Background(), client, "tbl1", map[string]any{
		"name": "priority", "type": "singleSelect",
	})
	if err != nil {
		t.Fatalf("CreateField: %v", err)
	}
	if !result.Success {
		t.Errorf("a clean create was refused: %v", result.Mismatches)
	}
}

// --- edit-field ------------------------------------------------------------

// The reported defect: PATCH /field returns 200, validates nothing, and
// applies nothing. This is the default outcome this test locks in: refused,
// because a fresh read-back shows the field never moved.
func TestEditFieldRefusesOnANoOpThatReturned200(t *testing.T) {
	t.Parallel()
	fake := newFakeTeable()
	unchanged := Field{"id": "fld1", "name": "old-name"}
	patched := false
	fake.mux.HandleFunc("/table/tbl1/field/fld1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			patched = true
			w.WriteHeader(http.StatusOK) // reports success and changes nothing
		}
	})
	fake.mux.HandleFunc("/table/tbl1/field", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []Field{unchanged})
	})
	fake.mux.HandleFunc("/table/tbl1/record", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, recordPage{})
	})
	srv := fake.server()
	defer srv.Close()
	client := &Client{BaseURL: srv.URL, Token: "t"}

	result, err := EditField(context.Background(), client, "tbl1", "fld1", map[string]any{"name": "new-name"})
	if err != nil {
		t.Fatalf("EditField: %v", err)
	}
	if !patched {
		t.Fatal("the PATCH was never sent")
	}
	if result.Success {
		t.Fatal("a no-op PATCH was reported as a successful edit")
	}
}

func TestEditFieldFlagsAnUnexpectedValueChange(t *testing.T) {
	t.Parallel()
	fake := newFakeTeable()
	renamed := Field{"id": "fld1", "name": "new-name"}
	callCount := 0
	fake.mux.HandleFunc("/table/tbl1/field/fld1", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	fake.mux.HandleFunc("/table/tbl1/field", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []Field{renamed})
	})
	fake.mux.HandleFunc("/table/tbl1/record", func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			writeJSON(w, recordPage{Records: []struct {
				ID     string         `json:"id"`
				Fields map[string]any `json:"fields"`
			}{{ID: "rec1", Fields: map[string]any{"fld1": "kept"}}}})
			return
		}
		// The value went missing on the second read, which a field rename
		// alone should never cause.
		writeJSON(w, recordPage{})
	})
	srv := fake.server()
	defer srv.Close()
	client := &Client{BaseURL: srv.URL, Token: "t"}

	result, err := EditField(context.Background(), client, "tbl1", "fld1", map[string]any{"name": "new-name"})
	if err != nil {
		t.Fatalf("EditField: %v", err)
	}
	if len(result.ValueChanges) != 1 || !result.ValueChanges[0].DataLoss {
		t.Errorf("ValueChanges = %+v, want one lost value flagged", result.ValueChanges)
	}
}

// --- convert-field -----------------------------------------------------------

// The reported defect: a convert emptied every value in the column while
// returning notNull true, exactly as requested. Metadata alone reads as a
// clean success; only the value snapshot catches it.
func TestConvertFieldDetectsDataLossTheMetadataHides(t *testing.T) {
	t.Parallel()
	fake := newFakeTeable()
	converted := Field{"id": "fld1", "notNull": true} // exactly what was requested
	callCount := 0
	fake.mux.HandleFunc("/table/tbl1/field/fld1/convert", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	fake.mux.HandleFunc("/table/tbl1/field", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []Field{converted})
	})
	fake.mux.HandleFunc("/table/tbl1/record", func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			writeJSON(w, recordPage{Records: []struct {
				ID     string         `json:"id"`
				Fields map[string]any `json:"fields"`
			}{
				{ID: "rec1", Fields: map[string]any{"fld1": "real value"}},
				{ID: "rec2", Fields: map[string]any{"fld1": "another"}},
			}})
			return
		}
		writeJSON(w, recordPage{Records: []struct {
			ID     string         `json:"id"`
			Fields map[string]any `json:"fields"`
		}{
			{ID: "rec1", Fields: map[string]any{"fld1": nil}},
			{ID: "rec2", Fields: map[string]any{"fld1": nil}},
		}})
	})
	srv := fake.server()
	defer srv.Close()
	client := &Client{BaseURL: srv.URL, Token: "t"}

	result, err := ConvertField(context.Background(), client, "tbl1", "fld1", map[string]any{"notNull": true}, false)
	if err != nil {
		t.Fatalf("ConvertField: %v", err)
	}
	if !result.Success {
		t.Errorf("mismatches = %v, want the metadata to read back exactly as requested", result.Mismatches)
	}
	if len(result.DataLoss) != 2 {
		t.Fatalf("DataLoss = %+v, want both rows flagged", result.DataLoss)
	}
	if len(result.Restored) != 0 {
		t.Error("restore ran without --restore-on-data-loss")
	}
}

// The recovery path: pre-convert values are written back through the record
// API, which this test's fake treats as reliable, matching what the sirens
// tracker adapter already established about record writes.
func TestConvertFieldRestoresLostValuesWhenAsked(t *testing.T) {
	t.Parallel()
	fake := newFakeTeable()
	callCount := 0
	restored := map[string]any{}
	fake.mux.HandleFunc("/table/tbl1/field/fld1/convert", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	fake.mux.HandleFunc("/table/tbl1/field", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []Field{{"id": "fld1"}})
	})
	fake.mux.HandleFunc("/table/tbl1/record", func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			writeJSON(w, recordPage{Records: []struct {
				ID     string         `json:"id"`
				Fields map[string]any `json:"fields"`
			}{{ID: "rec1", Fields: map[string]any{"fld1": "real value"}}}})
			return
		}
		writeJSON(w, recordPage{Records: []struct {
			ID     string         `json:"id"`
			Fields map[string]any `json:"fields"`
		}{{ID: "rec1", Fields: map[string]any{"fld1": nil}}}})
	})
	fake.mux.HandleFunc("/table/tbl1/record/rec1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("unexpected method %s restoring rec1", r.Method)
			return
		}
		var body struct {
			Record struct {
				Fields map[string]any `json:"fields"`
			} `json:"record"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		restored["rec1"] = body.Record.Fields["fld1"]
		w.WriteHeader(http.StatusOK)
	})
	srv := fake.server()
	defer srv.Close()
	client := &Client{BaseURL: srv.URL, Token: "t"}

	result, err := ConvertField(context.Background(), client, "tbl1", "fld1", map[string]any{}, true)
	if err != nil {
		t.Fatalf("ConvertField: %v", err)
	}
	if len(result.Restored) != 1 || result.Restored[0] != "rec1" {
		t.Errorf("Restored = %v, want rec1", result.Restored)
	}
	if restored["rec1"] != "real value" {
		t.Errorf("the record API received %v, want the pre-convert value", restored["rec1"])
	}
}

// --- pagination --------------------------------------------------------------

// SnapshotValues has to page past a single response: the reported constraint
// is no total in any response, so the end has to be a short page.
func TestSnapshotValuesPagesUntilAShortPage(t *testing.T) {
	t.Parallel()
	fake := newFakeTeable()
	pagesServed := 0
	fake.mux.HandleFunc("/table/tbl1/record", func(w http.ResponseWriter, r *http.Request) {
		pagesServed++
		skip := r.URL.Query().Get("skip")
		if skip == "0" {
			records := make([]struct {
				ID     string         `json:"id"`
				Fields map[string]any `json:"fields"`
			}, pageSize)
			for i := range records {
				records[i] = struct {
					ID     string         `json:"id"`
					Fields map[string]any `json:"fields"`
				}{ID: "rec" + string(rune('a'+i%26)) + r.URL.Query().Get("skip") + string(rune(i)), Fields: map[string]any{"fld1": "v"}}
			}
			writeJSON(w, recordPage{Records: records})
			return
		}
		writeJSON(w, recordPage{Records: []struct {
			ID     string         `json:"id"`
			Fields map[string]any `json:"fields"`
		}{{ID: "last", Fields: map[string]any{"fld1": "v"}}}})
	})
	srv := fake.server()
	defer srv.Close()
	client := &Client{BaseURL: srv.URL, Token: "t"}

	values, err := client.SnapshotValues(context.Background(), "tbl1", "fld1")
	if err != nil {
		t.Fatalf("SnapshotValues: %v", err)
	}
	if pagesServed != 2 {
		t.Errorf("pagesServed = %d, want exactly 2 (one full, one short)", pagesServed)
	}
	if len(values) != pageSize+1 {
		t.Errorf("values = %d rows, want %d", len(values), pageSize+1)
	}
}
