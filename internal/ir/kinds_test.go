package ir

import (
	"reflect"
	"sort"
	"testing"

	"github.com/inth3shadows/runecho/internal/parser"
)

// TestSymbolKindsMatchEmitted pins SymbolKinds to what symbolsFromStructure can
// actually mint (#365). Every []string field of parser.FileStructure is filled
// by reflection, so a new name list added there and emitted under a new kind
// fails here until SymbolKinds — and the docs that point at it — learn it. The
// SymbolDelta comment listed four kinds while eight were emitted; that is the
// drift this closes.
func TestSymbolKindsMatchEmitted(t *testing.T) {
	var fs parser.FileStructure
	v := reflect.ValueOf(&fs).Elem()
	strSlice := reflect.TypeOf([]string(nil))
	for i := 0; i < v.NumField(); i++ {
		if f := v.Field(i); f.Type() == strSlice && f.CanSet() {
			f.Set(reflect.ValueOf([]string{"N" + v.Type().Field(i).Name}))
		}
	}
	// A Python source with one import, so importedNames yields import_name.
	syms := symbolsFromStructure(fs, "x.py", "from m import Z\n")

	emitted := map[string]bool{}
	for _, s := range syms {
		emitted[s.Kind] = true
	}
	known := map[string]bool{}
	for _, k := range SymbolKinds {
		known[k] = true
	}
	var extra, missing []string
	for k := range emitted {
		if !known[k] {
			extra = append(extra, k)
		}
	}
	for k := range known {
		if !emitted[k] {
			missing = append(missing, k)
		}
	}
	sort.Strings(extra)
	sort.Strings(missing)
	if len(extra) > 0 {
		t.Errorf("symbolsFromStructure emits kinds absent from SymbolKinds: %v", extra)
	}
	if len(missing) > 0 {
		t.Errorf("SymbolKinds lists kinds symbolsFromStructure never emits: %v", missing)
	}
}
