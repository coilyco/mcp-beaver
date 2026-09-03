package teableadmin

import "reflect"

// fieldMismatches compares every key in a request against a fresh read-back
// of the field it produced. This is the whole of the create-field defect:
// Teable accepts unknown properties and silently discards them, so a field
// requested with five properties can come back with three, and only a
// key-by-key diff against a re-read finds the two that vanished.
func fieldMismatches(requested map[string]any, actual Field) []string {
	mismatches := make([]string, 0)
	for key, want := range requested {
		got, present := actual[key]
		if !present {
			mismatches = append(mismatches, key+": requested but absent from the read-back")
			continue
		}
		if !reflect.DeepEqual(normalizeJSON(want), normalizeJSON(got)) {
			mismatches = append(mismatches, key+": requested differs from the read-back")
		}
	}
	return mismatches
}

// normalizeJSON collapses the numeric-type differences a JSON round trip
// introduces (an int requested, a float64 decoded) so the diff compares
// values rather than Go types neither side chose.
func normalizeJSON(v any) any {
	switch n := v.(type) {
	case int:
		return float64(n)
	case int64:
		return float64(n)
	default:
		return v
	}
}

// ValueDiff is what changed in one record's value for the field under audit.
type ValueDiff struct {
	RecordID string
	Before   any
	After    any
	// DataLoss is set when Before held a real value and After does not: the
	// shape of Teable's confirmed defect 5, a convert that emptied a column
	// while reporting success. A value that changed between two other
	// non-empty values is recorded but not flagged as loss, because that is
	// what a working type conversion is supposed to do.
	DataLoss bool
}

// diffValues compares a before and after snapshot keyed by record id.
// A record present in before and absent from after is treated as byte loss
// of that row's value: the value existed, and there is nothing to compare it
// against, which is the same "no evidence it survived" shape as the row
// reading back empty.
func diffValues(before, after map[string]any) []ValueDiff {
	diffs := make([]ValueDiff, 0)
	for recordID, wasValue := range before {
		isValue, present := after[recordID]
		if !present {
			isValue = nil
		}
		if reflect.DeepEqual(normalizeJSON(wasValue), normalizeJSON(isValue)) {
			continue
		}
		diffs = append(diffs, ValueDiff{
			RecordID: recordID,
			Before:   wasValue,
			After:    isValue,
			DataLoss: isEmpty(wasValue) == false && isEmpty(isValue),
		})
	}
	return diffs
}

// isEmpty treats nil, an empty string, and an empty slice as no value. A
// zero number or false boolean is a real value and is never empty.
func isEmpty(v any) bool {
	if v == nil {
		return true
	}
	switch t := v.(type) {
	case string:
		return t == ""
	case []any:
		return len(t) == 0
	default:
		return false
	}
}
