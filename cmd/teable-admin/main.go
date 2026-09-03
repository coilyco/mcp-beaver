// Command teable-admin runs the three field-schema verbs the guarded MCP
// surface withholds, because they need imperative read-verify-restore logic
// no guardfile's single-call allowlist can express: create with a read-back
// that catches Teable silently discarding requested properties, edit with a
// read-back that catches its own no-op success, and convert with a value
// snapshot taken first so destroyed data can be restored through the
// record API, which is reliable where the field API is not.
//
// This talks to Teable directly and never through mcp-beaver's guarded proxy.
// Run it deliberately, never from an agent turn: there is no delete-field
// verb, so a bad create-field leaves a stray field only the Teable UI can
// remove, and convert-field is irreversible on Teable's side regardless of
// what this tool does afterward.
//
//	teable-admin create-field --table <id> --spec <spec.json>
//	teable-admin edit-field --table <id> --field <id> --patch <patch.json>
//	teable-admin convert-field --table <id> --field <id> --spec <spec.json> [--restore-on-data-loss]
//	teable-admin list-fields --table <id>
//
// TEABLE_BASE_URL and TEABLE_API_TOKEN are required, read from the
// environment rather than a flag so a token never appears in a shell history
// or a process list.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"forgejo.coilysiren.me/coilyco-flight-deck/mcp-beaver/internal/teableadmin"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "teable-admin:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("usage: teable-admin create-field | edit-field | convert-field | list-fields (see -h)")
	}
	switch argv[0] {
	case "create-field":
		return runCreateField(ctx, argv[1:])
	case "edit-field":
		return runEditField(ctx, argv[1:])
	case "convert-field":
		return runConvertField(ctx, argv[1:])
	case "list-fields":
		return runListFields(ctx, argv[1:])
	default:
		return fmt.Errorf("unknown command %q (want: create-field, edit-field, convert-field, list-fields)", argv[0])
	}
}

// clientFromEnv builds the Teable client from the two required env vars,
// named so a missing one says which rather than failing on a nil token.
func clientFromEnv() (*teableadmin.Client, error) {
	baseURL := os.Getenv("TEABLE_BASE_URL")
	if baseURL == "" {
		return nil, fmt.Errorf("TEABLE_BASE_URL is required")
	}
	token := os.Getenv("TEABLE_API_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("TEABLE_API_TOKEN is required")
	}
	return &teableadmin.Client{BaseURL: baseURL, Token: token}, nil
}

// readJSONFile decodes a flag-supplied path into a map, named in every error
// so a malformed spec file says which flag pointed at it.
func readJSONFile(flagName, path string) (map[string]any, error) {
	if path == "" {
		return nil, fmt.Errorf("--%s is required", flagName)
	}
	raw, err := os.ReadFile(path) //nolint:gosec // operator-supplied trusted path
	if err != nil {
		return nil, fmt.Errorf("read --%s %q: %w", flagName, path, err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("parse --%s %q: %w", flagName, path, err)
	}
	return body, nil
}

func runCreateField(ctx context.Context, argv []string) error {
	fs := flag.NewFlagSet("create-field", flag.ContinueOnError)
	table := fs.String("table", "", "table id")
	specPath := fs.String("spec", "", "path to a JSON file with the field's requested properties")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if *table == "" {
		return fmt.Errorf("--table is required")
	}
	spec, err := readJSONFile("spec", *specPath)
	if err != nil {
		return err
	}
	client, err := clientFromEnv()
	if err != nil {
		return err
	}
	result, err := teableadmin.CreateField(ctx, client, *table, spec)
	if err != nil {
		return err
	}
	if !result.Success {
		fmt.Fprintf(os.Stdout, "REFUSED: field %s exists but does not match what was requested\n", result.FieldID)
		for _, m := range result.Mismatches {
			fmt.Fprintln(os.Stdout, "  "+m)
		}
		fmt.Fprintln(os.Stdout, "Nothing was deleted: there is no delete-field verb. Remove it by hand in the Teable UI if it should not exist.")
		return fmt.Errorf("create-field refused: %d field(s) did not survive the round trip", len(result.Mismatches))
	}
	fmt.Fprintf(os.Stdout, "created and confirmed field %s\n", result.FieldID)
	return nil
}

func runEditField(ctx context.Context, argv []string) error {
	fs := flag.NewFlagSet("edit-field", flag.ContinueOnError)
	table := fs.String("table", "", "table id")
	field := fs.String("field", "", "field id")
	patchPath := fs.String("patch", "", "path to a JSON file with the requested field properties")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if *table == "" || *field == "" {
		return fmt.Errorf("--table and --field are both required")
	}
	patch, err := readJSONFile("patch", *patchPath)
	if err != nil {
		return err
	}
	client, err := clientFromEnv()
	if err != nil {
		return err
	}
	result, err := teableadmin.EditField(ctx, client, *table, *field, patch)
	if err != nil {
		return err
	}
	for _, v := range result.ValueChanges {
		fmt.Fprintf(os.Stdout, "value changed on record %s: %v -> %v (data loss: %v)\n", v.RecordID, v.Before, v.After, v.DataLoss)
	}
	if !result.Success {
		fmt.Fprintln(os.Stdout, "REFUSED: the field's definition does not match what was requested")
		for _, m := range result.Mismatches {
			fmt.Fprintln(os.Stdout, "  "+m)
		}
		fmt.Fprintln(os.Stdout, "This is Teable's documented behavior for this endpoint: it can return 200, validate nothing, and apply nothing. The field almost certainly did not move.")
		return fmt.Errorf("edit-field refused: %d field(s) did not survive the round trip", len(result.Mismatches))
	}
	fmt.Fprintln(os.Stdout, "edited and confirmed the field definition")
	return nil
}

func runConvertField(ctx context.Context, argv []string) error {
	fs := flag.NewFlagSet("convert-field", flag.ContinueOnError)
	table := fs.String("table", "", "table id")
	field := fs.String("field", "", "field id")
	specPath := fs.String("spec", "", "path to a JSON file with the requested field properties")
	restore := fs.Bool("restore-on-data-loss", false, "write pre-convert values back through the record API where the convert emptied them")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if *table == "" || *field == "" {
		return fmt.Errorf("--table and --field are both required")
	}
	spec, err := readJSONFile("spec", *specPath)
	if err != nil {
		return err
	}
	client, err := clientFromEnv()
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "snapshotting every row's value before converting: this is the recovery copy, keep it if this command does not finish")
	result, err := teableadmin.ConvertField(ctx, client, *table, *field, spec, *restore)
	if err != nil {
		return err
	}
	if len(result.DataLoss) == 0 {
		fmt.Fprintln(os.Stdout, "no rows lost a value")
	} else {
		fmt.Fprintf(os.Stdout, "DATA LOSS: %d row(s) held a value before and do not after\n", len(result.DataLoss))
		for _, d := range result.DataLoss {
			fmt.Fprintf(os.Stdout, "  %s: %v -> empty\n", d.RecordID, d.Before)
		}
		if *restore {
			fmt.Fprintf(os.Stdout, "restored %d row(s) through the record API\n", len(result.Restored))
			if len(result.RestoreFailures) > 0 {
				fmt.Fprintf(os.Stdout, "FAILED TO RESTORE %d row(s), values are in this run's output above, above, restore them by hand:\n", len(result.RestoreFailures))
				for _, f := range result.RestoreFailures {
					fmt.Fprintln(os.Stdout, "  "+f)
				}
			}
		} else {
			fmt.Fprintln(os.Stdout, "not restored: rerun with --restore-on-data-loss, using the values printed above")
		}
	}
	if !result.Success {
		fmt.Fprintln(os.Stdout, "REFUSED: the field's definition does not match what was requested, independent of any data loss above")
		for _, m := range result.Mismatches {
			fmt.Fprintln(os.Stdout, "  "+m)
		}
		return fmt.Errorf("convert-field refused: %d field(s) did not survive the round trip", len(result.Mismatches))
	}
	if len(result.DataLoss) > 0 && (!*restore || len(result.RestoreFailures) > 0) {
		return fmt.Errorf("convert-field applied but lost data: see output above")
	}
	fmt.Fprintln(os.Stdout, "converted and confirmed the field definition")
	return nil
}

func runListFields(ctx context.Context, argv []string) error {
	fs := flag.NewFlagSet("list-fields", flag.ContinueOnError)
	table := fs.String("table", "", "table id")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if *table == "" {
		return fmt.Errorf("--table is required")
	}
	client, err := clientFromEnv()
	if err != nil {
		return err
	}
	fields, err := client.ListFields(ctx, *table)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	for _, f := range fields {
		if err := encoder.Encode(f); err != nil {
			return err
		}
	}
	return nil
}
