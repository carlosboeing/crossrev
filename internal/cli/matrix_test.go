package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// matrixRow is one command line and what the parser must make of it.
//
// A row either parses — Command and Request are set — or it is refused. A
// refusal is either the two strings ui_die prints (Reason and Action) or the
// shell's silent stop, which prints nothing at all and exits 1.
type matrixRow struct {
	Name    string            `json:"name"`
	Args    []string          `json:"args"`
	Command string            `json:"command"`
	Request map[string]string `json:"request"`
	Reason  string            `json:"reason"`
	Action  string            `json:"action"`
	Silent  bool              `json:"silent"`
}

type matrixFile struct {
	Harnesses []string    `json:"harnesses"`
	Rows      []matrixRow `json:"rows"`
}

func loadMatrix(t *testing.T) matrixFile {
	t.Helper()
	raw, err := os.ReadFile("testdata/matrix.json")
	if err != nil {
		t.Fatalf("reading the matrix: %v", err)
	}
	var file matrixFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("parsing the matrix: %v", err)
	}
	if len(file.Rows) == 0 {
		t.Fatal("the matrix is empty, so it proves nothing")
	}
	return file
}

// captureIO is an IO whose two streams are buffers, so a test reads what a
// reader would have seen.
func captureIO() (*ui.IO, *bytes.Buffer, *bytes.Buffer) {
	var out, errs bytes.Buffer
	return &ui.IO{Out: &out, Err: &errs, Palette: ui.Plain()}, &out, &errs
}

// fieldStrings renders a parsed request as a flat map, by reflection rather
// than by a hand-written mapping per request type. A field added to a request
// and forgotten in the matrix then fails the row it belongs to, which a
// hand-written mapping would not.
func fieldStrings(t *testing.T, request any) map[string]string {
	t.Helper()
	v := reflect.ValueOf(request)
	if v.Kind() != reflect.Struct {
		t.Fatalf("a parsed request must be a struct, and %T is not", request)
	}
	out := make(map[string]string, v.NumField())
	for i := range v.NumField() {
		field := v.Type().Field(i)
		if !field.IsExported() {
			t.Fatalf("%s.%s is unexported, so the matrix cannot assert it", v.Type(), field.Name)
		}
		out[field.Name] = renderField(t, v.Field(i))
	}
	return out
}

func renderField(t *testing.T, v reflect.Value) string {
	t.Helper()
	switch value := v.Interface().(type) {
	case core.Slug:
		if value.Incomplete() {
			return ""
		}
		return value.String()
	case time.Duration:
		return strconv.FormatInt(int64(value/time.Second), 10)
	case string:
		return value
	case bool:
		return strconv.FormatBool(value)
	case int:
		return strconv.Itoa(value)
	}
	t.Fatalf("the matrix has no rendering for %s", v.Type())
	return ""
}
