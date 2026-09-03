package teableadmin

import (
	"context"
	"fmt"
)

// CreateResult is what CreateField found, always returned even on a refusal,
// so a caller can render the mismatches that caused it.
type CreateResult struct {
	FieldID    string
	Success    bool
	Mismatches []string
}

// CreateField posts a field, re-reads it independently, and refuses success
// unless every requested property survived. It never retries and never
// deletes the field it created on a mismatch: there is no delete-field verb,
// so a caller who gets a refusal removes the stray field in the Teable UI.
func CreateField(ctx context.Context, client *Client, tableID string, spec map[string]any) (CreateResult, error) {
	created, err := client.CreateField(ctx, tableID, spec)
	if err != nil {
		return CreateResult{}, fmt.Errorf("create field: %w", err)
	}
	fieldID := created.ID()
	if fieldID == "" {
		return CreateResult{}, fmt.Errorf("create field: response carried no id, cannot read it back")
	}
	reread, found, err := client.FieldByID(ctx, tableID, fieldID)
	if err != nil {
		return CreateResult{}, fmt.Errorf("read back created field %s: %w", fieldID, err)
	}
	if !found {
		return CreateResult{FieldID: fieldID}, fmt.Errorf(
			"create field: the tracker reported %s created, but it does not appear in a fresh field list", fieldID,
		)
	}
	mismatches := fieldMismatches(spec, reread)
	return CreateResult{
		FieldID:    fieldID,
		Success:    len(mismatches) == 0,
		Mismatches: mismatches,
	}, nil
}

// EditResult is what EditField found. ValueChanges is always populated, even
// on success, because a field write is not supposed to touch stored values
// and any change there is worth surfacing regardless of the schema outcome.
type EditResult struct {
	Success      bool
	Mismatches   []string
	ValueChanges []ValueDiff
}

// EditField snapshots the column's values, PATCHes the field, re-reads the
// field definition independently, and snapshots the values again. Teable's
// documented defect here is a 200 that validates and applies nothing, so the
// expected honest outcome most of the time is Success: false with no
// ValueChanges — the field did not move, and this says so instead of the API
// claiming otherwise.
func EditField(
	ctx context.Context, client *Client, tableID, fieldID string, patch map[string]any,
) (EditResult, error) {
	before, err := client.SnapshotValues(ctx, tableID, fieldID)
	if err != nil {
		return EditResult{}, fmt.Errorf("snapshot values before edit: %w", err)
	}
	if err := client.EditField(ctx, tableID, fieldID, patch); err != nil {
		return EditResult{}, fmt.Errorf("edit field: %w", err)
	}
	reread, found, err := client.FieldByID(ctx, tableID, fieldID)
	if err != nil {
		return EditResult{}, fmt.Errorf("read back edited field %s: %w", fieldID, err)
	}
	if !found {
		return EditResult{}, fmt.Errorf(
			"edit field: %s no longer appears in a fresh field list after the edit", fieldID,
		)
	}
	after, err := client.SnapshotValues(ctx, tableID, fieldID)
	if err != nil {
		return EditResult{}, fmt.Errorf("snapshot values after edit: %w", err)
	}
	return EditResult{
		Success:      len(fieldMismatches(patch, reread)) == 0,
		Mismatches:   fieldMismatches(patch, reread),
		ValueChanges: diffValues(before, after),
	}, nil
}

// ConvertResult is what ConvertField found, including what it restored.
type ConvertResult struct {
	Success    bool
	Mismatches []string
	DataLoss   []ValueDiff
	Restored   []string
	// RestoreFailures names a record RestoreValue could not write, so lost
	// data is never reported as recovered when it was not.
	RestoreFailures []string
}

// ConvertField is the one operation this package cannot make safe, only
// accountable. Teable's own convert has emptied a column while reporting
// success (defect 5), and nothing client-side stops that from happening
// again: the damage, if any, is already done by the time this function's
// first read-back runs. What it can do is know immediately rather than
// discover it later, and restore from the snapshot it took first through
// record-level writes, which are reliable where field-level writes are not.
//
// restore controls whether a detected loss is written back automatically.
// False leaves DataLoss populated for the caller to act on by hand.
func ConvertField(
	ctx context.Context, client *Client, tableID, fieldID string, spec map[string]any, restore bool,
) (ConvertResult, error) {
	before, err := client.SnapshotValues(ctx, tableID, fieldID)
	if err != nil {
		return ConvertResult{}, fmt.Errorf("snapshot values before convert: %w", err)
	}
	if err := client.ConvertField(ctx, tableID, fieldID, spec); err != nil {
		return ConvertResult{}, fmt.Errorf("convert field: %w", err)
	}
	reread, found, err := client.FieldByID(ctx, tableID, fieldID)
	if err != nil {
		return ConvertResult{}, fmt.Errorf("read back converted field %s: %w", fieldID, err)
	}
	if !found {
		return ConvertResult{}, fmt.Errorf(
			"convert field: %s no longer appears in a fresh field list after the convert", fieldID,
		)
	}
	after, err := client.SnapshotValues(ctx, tableID, fieldID)
	if err != nil {
		return ConvertResult{}, fmt.Errorf("snapshot values after convert: %w", err)
	}
	changes := diffValues(before, after)
	loss := make([]ValueDiff, 0, len(changes))
	for _, d := range changes {
		if d.DataLoss {
			loss = append(loss, d)
		}
	}
	result := ConvertResult{
		Success:    len(fieldMismatches(spec, reread)) == 0,
		Mismatches: fieldMismatches(spec, reread),
		DataLoss:   loss,
	}
	if !restore || len(loss) == 0 {
		return result, nil
	}
	for _, d := range loss {
		if err := client.RestoreValue(ctx, tableID, d.RecordID, fieldID, d.Before); err != nil {
			result.RestoreFailures = append(result.RestoreFailures, fmt.Sprintf("%s: %v", d.RecordID, err))
			continue
		}
		result.Restored = append(result.Restored, d.RecordID)
	}
	return result, nil
}
