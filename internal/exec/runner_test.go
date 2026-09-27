package exec_test

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
)

func TestResultOK(t *testing.T) {
	tests := []struct {
		name   string
		result exec.Result
		want   bool
	}{
		{name: "clean zero exit", result: exec.Result{ExitCode: 0}, want: true},
		{name: "non-zero exit", result: exec.Result{ExitCode: 1}, want: false},
		{name: "zero exit with error", result: exec.Result{ExitCode: 0, Err: errors.New("failed")}, want: false},
		{name: "signal exit", result: exec.Result{ExitCode: 130}, want: false},
		{name: "start error", result: exec.Result{ExitCode: -1, Err: errors.New("cannot start")}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.result.OK(); got != tt.want {
				t.Errorf("Result.OK() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestResultSignaled(t *testing.T) {
	tests := []struct {
		name   string
		result exec.Result
		want   bool
	}{
		{name: "explicit signal SIGINT", result: exec.Result{Signal: syscall.SIGINT}, want: true},
		{name: "explicit signal SIGTERM", result: exec.Result{Signal: syscall.SIGTERM}, want: true},
		{name: "mapped exit code 130 (SIGINT)", result: exec.Result{ExitCode: 130}, want: true},
		{name: "mapped exit code 131 (SIGQUIT)", result: exec.Result{ExitCode: 131}, want: true},
		{name: "mapped exit code 137 (SIGKILL)", result: exec.Result{ExitCode: 137}, want: true},
		{name: "mapped exit code 139 (SIGSEGV)", result: exec.Result{ExitCode: 139}, want: true},
		{name: "mapped exit code 143 (SIGTERM)", result: exec.Result{ExitCode: 143}, want: true},
		{name: "clean exit 0", result: exec.Result{ExitCode: 0}, want: false},
		{name: "ordinary exit 1", result: exec.Result{ExitCode: 1}, want: false},
		{name: "ordinary exit 2", result: exec.Result{ExitCode: 2}, want: false},
		{name: "exit 127", result: exec.Result{ExitCode: 127}, want: false},
		{name: "boundary exit 128", result: exec.Result{ExitCode: 128}, want: false},
		{name: "start failure -1", result: exec.Result{ExitCode: -1}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.result.Signaled(); got != tt.want {
				t.Errorf("Result.Signaled() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestResultInterrupted(t *testing.T) {
	tests := []struct {
		name   string
		result exec.Result
		want   bool
	}{
		{name: "exit 130 (SIGINT)", result: exec.Result{ExitCode: 130}, want: true},
		{name: "exit 131 (SIGQUIT)", result: exec.Result{ExitCode: 131}, want: true},
		{name: "exit 137 (SIGKILL)", result: exec.Result{ExitCode: 137}, want: true},
		{name: "exit 143 (SIGTERM)", result: exec.Result{ExitCode: 143}, want: true},
		{name: "signal SIGINT", result: exec.Result{Signal: syscall.SIGINT}, want: true},
		{name: "signal SIGKILL", result: exec.Result{Signal: syscall.SIGKILL}, want: true},
		{name: "signal SIGTERM", result: exec.Result{Signal: syscall.SIGTERM}, want: true},
		{name: "signal os.Interrupt", result: exec.Result{Signal: os.Interrupt}, want: true},
		{name: "signal os.Kill", result: exec.Result{Signal: os.Kill}, want: true},
		{name: "context canceled", result: exec.Result{Err: context.Canceled}, want: true},
		{name: "clean exit 0", result: exec.Result{ExitCode: 0}, want: false},
		{name: "harness failure exit 1", result: exec.Result{ExitCode: 1}, want: false},
		{name: "harness failure exit 2", result: exec.Result{ExitCode: 2}, want: false},
		{name: "exit 127", result: exec.Result{ExitCode: 127}, want: false},
		{name: "crash exit 139 (SIGSEGV)", result: exec.Result{ExitCode: 139}, want: false},
		{name: "crash signal SIGSEGV", result: exec.Result{Signal: syscall.SIGSEGV}, want: false},
		{name: "start failure -1", result: exec.Result{ExitCode: -1, Err: errors.New("cannot start")}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.result.Interrupted(); got != tt.want {
				t.Errorf("Result.Interrupted() = %t, want %t", got, tt.want)
			}
		})
	}
}
