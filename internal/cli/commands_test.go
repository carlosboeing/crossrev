package cli

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// dispatched is one call a recorder saw.
type dispatched struct {
	field   string
	request any
}

// recordingCommands fills every field of Commands with a function that records
// which field was called and what it was handed, and then answers code and err.
//
// The fields are filled by reflection rather than one at a time, so a command
// added to the table is exercised by every test below without any of them being
// edited — and TestCommandsDispatchesEveryField fails until a row reaches it.
func recordingCommands(t *testing.T, code int, err error) (Commands, *[]dispatched) {
	t.Helper()
	var seen []dispatched
	var cmds Commands
	v := reflect.ValueOf(&cmds).Elem()
	for i := range v.NumField() {
		name := v.Type().Field(i).Name
		field := v.Field(i)
		if field.Kind() != reflect.Func {
			t.Fatalf("Commands.%s is %s, and every field of the table must be a func", name, field.Kind())
		}
		field.Set(reflect.MakeFunc(field.Type(), func(args []reflect.Value) []reflect.Value {
			seen = append(seen, dispatched{field: name, request: args[1].Interface()})
			out := []reflect.Value{reflect.ValueOf(code), reflect.Zero(field.Type().Out(1))}
			if err != nil {
				out[1] = reflect.ValueOf(err)
			}
			return out
		}))
	}
	return cmds, &seen
}

// fieldFor turns a command name into the field of Commands that must serve it:
// "auth login" is AuthLogin. The derivation is the test's, so a field named
// anything else fails rather than being quietly unreachable.
func fieldFor(name Command) string {
	var b strings.Builder
	for _, word := range strings.Fields(string(name)) {
		b.WriteString(strings.ToUpper(word[:1]))
		b.WriteString(word[1:])
	}
	return b.String()
}

// TestCommandsDispatchesEveryField walks every matrix row that parses and
// checks it reaches the field of Commands its name implies, holding the request
// the parser produced.
func TestCommandsDispatchesEveryField(t *testing.T) {
	file := loadMatrix(t)
	reached := make(map[string]bool)

	for _, row := range file.Rows {
		if row.Command == "" {
			continue
		}
		t.Run(row.Name, func(t *testing.T) {
			cmds, seen := recordingCommands(t, 0, nil)
			io, _, errs := captureIO()
			inv, err := Parse(row.Args, io, file.Harnesses)
			if err != nil {
				t.Fatalf("Parse(%q) = %v", row.Args, err)
			}
			code, err := Dispatch(context.Background(), inv, cmds, io)
			if err != nil || code != 0 {
				t.Fatalf("Dispatch = (%d, %v), stderr %q", code, err, errs)
			}
			if len(*seen) != 1 {
				t.Fatalf("Dispatch made %d calls, want exactly one", len(*seen))
			}
			got := (*seen)[0]
			want := fieldFor(inv.Command)
			if got.field != want {
				t.Errorf("%q reached Commands.%s, want Commands.%s", row.Command, got.field, want)
			}
			if !reflect.DeepEqual(got.request, inv.Request) {
				t.Errorf("Commands.%s got %#v, want the parsed %#v", got.field, got.request, inv.Request)
			}
			reached[got.field] = true
		})
	}

	var cmds Commands
	v := reflect.ValueOf(&cmds).Elem()
	for i := range v.NumField() {
		if name := v.Type().Field(i).Name; !reached[name] {
			t.Errorf("no matrix row reaches Commands.%s", name)
		}
	}
}

// TestCommandsRefusesAnUnwiredField pins what happens when the composition root
// leaves a field nil. It is a wiring fault with no counterpart in the shell,
// where a missing case arm cannot be built; it must refuse rather than panic,
// because a panic in a command-line tool is indistinguishable from a crash.
func TestCommandsRefusesAnUnwiredField(t *testing.T) {
	io, _, errs := captureIO()
	inv, err := Parse([]string{"status", "--pr", "3"}, io, nil)
	if err != nil {
		t.Fatalf("Parse = %v", err)
	}
	code, err := Dispatch(context.Background(), inv, Commands{}, io)
	if err == nil {
		t.Fatal("Dispatch accepted an unwired command")
	}
	if code != ExitFailure {
		t.Errorf("code = %d, want %d", code, ExitFailure)
	}
	if !strings.Contains(errs.String(), "status") {
		t.Errorf("stderr = %q, want it to name the command", errs)
	}
}
