package loc

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

type execResult struct {
	exitCode int
	output   []byte
	err      error
}

func (eres *execResult) checkOut(t *testing.T, needStrings map[string]bool) {
	for target, want := range needStrings {
		switch {
		case !strings.Contains(string(eres.output), target) && want:
			t.Errorf("output does not contain: %v", target)
		case strings.Contains(string(eres.output), target) && !want:
			t.Errorf("output contains unwanted: %v", target)
		default:
			//
		}
	}
}

func TestInspect(t *testing.T) {
	baseCmd := strings.Fields("go run ../../cmd/goloc/main.go")

	type test struct {
		name        string
		cmdSlice    []string
		exitCode    int
		needStrings map[string]bool
	}

	tests := []test{
		{
			name:     "inspect",
			cmdSlice: strings.Fields("inspect -l en-US -v ../../pkg/loc/test_data/"),
			exitCode: 0,
			needStrings: map[string]bool{
				`"hello there"`:      true,
				`"hello there %s"`:   true,
				`"in a format call"`: true,
			},
		},
	}

	for _, tc := range tests {

		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			res := make(chan execResult, 1)

			go func() {
				defer cancel()
				cmd := exec.Command(baseCmd[0], append(baseCmd[1:], tc.cmdSlice...)...)
				out, err := cmd.CombinedOutput()
				res <- execResult{
					exitCode: cmd.ProcessState.ExitCode(),
					output:   out,
					err:      err,
				}
			}()

			select {
			case <-ctx.Done():
				t.Fatal("timeout")
			case eRes := <-res:
				if eRes.err != nil {
					t.Errorf("cmd.CombinedOutput() error = %v", eRes.err)
				}
				if got, want := eRes.exitCode, tc.exitCode; got != want {
					t.Errorf("cmd.ProcessState.ExitCode() = %d, want %d", got, want)
				}
				if len(eRes.output) > 0 {
					t.Logf("\n-----output-----\n%s----------------\n", string(eRes.output))
				}
				if len(tc.needStrings) == 0 {
					return
				}
				eRes.checkOut(t, tc.needStrings)
			}
		})
	}
}
