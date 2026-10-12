package bridge

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/store"
)

// Every refusal is final, and a drive failure retries only when no agent
// work can have started: a started or ambiguous turn retried would run the
// member's prompt twice (and reserve a borrow twice). The failure reason is
// decided in job_failure.go only.
func TestDriveRefusalsAreFinal(t *testing.T) {
	for _, k := range refusalKinds {
		if k.reason.Retryable() {
			t.Errorf("refusal %s is retryable: a deterministic refusal fails the same way again", k.reason)
		}
		if k.notice == "" {
			t.Errorf("refusal %s posts no notice: the member would not see why nothing ran", k.reason)
		}
	}
	if store.FailureReplyTimeout.Retryable() {
		t.Error("reply_timeout is retryable: the turn ran")
	}

	dial := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	httpErr := func(status int) error { return &corepkg.HTTPError{Core: "c", Status: status} }
	cases := []struct {
		name string
		err  error
		want store.AgentJobFailureReason
	}{
		{"prepare: core unreachable", atStage(stagePrepare, dial), store.FailureCoreUnavailable},
		{"prepare: core not running yet", atStage(stagePrepare, coreNotRunningError{core: "acme"}), store.FailureCoreUnavailable},
		{"prepare: sandbox busy", atStage(stagePrepare, httpErr(http.StatusServiceUnavailable)), store.FailureCoreUnavailable},
		{"prepare: deterministic", atStage(stagePrepare, errors.New("the core no longer supports sandbox execution")), store.FailureRunnerFailed},
		{"prepare: sandbox not up yet", atStage(stagePrepare, fmt.Errorf("the agent sandbox could not be prepared: %w", sandboxNotReadyError{err: dial})), store.FailureSandboxStarting},
		{"start: never reached the core", atStage(stageStart, dial), store.FailureRunnerStartFailed},
		{"start: core refused before processing", atStage(stageStart, httpErr(http.StatusTooManyRequests)), store.FailureRunnerStartFailed},
		{"start: ambiguous 500", atStage(stageStart, httpErr(http.StatusInternalServerError)), store.FailureRunnerFailed},
		{"start: ambiguous 502", atStage(stageStart, httpErr(http.StatusBadGateway)), store.FailureRunnerFailed},
		{"start: reset mid-response", atStage(stageStart, io.ErrUnexpectedEOF), store.FailureRunnerFailed},
		{"start: timed out", atStage(stageStart, context.DeadlineExceeded), store.FailureRunnerFailed},
		{"start inside ensure keeps its stage", atStage(stagePrepare, atStage(stageStart, fmt.Errorf("create session: %w", dial))), store.FailureRunnerStartFailed},
		{"run: started turn, core gone", atStage(stageRun, dial), store.FailureRunnerFailed},
		{"run: killed", atStage(stageRun, errors.New("runner killed by signal 9")), store.FailureRunnerFailed},
		{"unknown stage", dial, store.FailureRunnerFailed},
	}
	for _, c := range cases {
		got := driveFailureReason(c.err)
		if got != c.want {
			t.Errorf("%s: reason %s, want %s", c.name, got, c.want)
		}
		var de *driveError
		if errors.As(c.err, &de) && de.stage == stageRun && got.Retryable() {
			t.Errorf("%s: a started turn maps to retryable %s", c.name, got)
		}
	}

	// One owner: no other file reports a failed job or declares a refusal.
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	declared := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CompositeLit:
				if id, ok := n.Type.(*ast.Ident); ok && (id.Name == "refusalKind" || id.Name == "refusal") {
					if f != "job_failure.go" {
						t.Errorf("%s: %s declared outside job_failure.go", fset.Position(n.Pos()), id.Name)
					}
					if id.Name == "refusalKind" {
						declared++
					}
				}
			case *ast.CallExpr:
				fn, ok := n.Fun.(*ast.SelectorExpr)
				if !ok || fn.Sel.Name != "reportJobStatus" || f == "job_failure.go" {
					return true
				}
				for _, a := range n.Args {
					if s, ok := a.(*ast.SelectorExpr); ok && s.Sel.Name == "AgentJobFailed" {
						t.Errorf("%s: a failed job status reported outside job_failure.go (use refuse / failJob)", fset.Position(n.Pos()))
					}
				}
			}
			return true
		})
	}
	if declared != len(refusalKinds) {
		t.Errorf("job_failure.go declares %d refusal kinds, refusalKinds lists %d", declared, len(refusalKinds))
	}
}

// A drive whose start request is ambiguous (the core answered 500: it may
// have run the turn) fails final; one whose core was never reached fails
// retryably.
func TestDriveStartFailureClassification(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want string
	}{
		{"ambiguous", &corepkg.HTTPError{Core: "core-a", Status: http.StatusInternalServerError}, "failed:runner_failed"},
		{"never reached", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}, "failed:runner_start_failed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			gw := newSandboxGateway(t, "room-1", false)
			prov := &sandboxProv{supports: true, createErr: c.err}
			b, _ := sandboxBridge(t, gw, nil, SandboxBorrowed, prov)
			b.runBridgeJob(context.Background(), job("job-1", "alice"))
			st := gw.lastStatus()
			if got := fmt.Sprint(st["status"]) + ":" + fmt.Sprint(st["failureReason"]); got != c.want {
				t.Fatalf("status = %s, want %s", got, c.want)
			}
		})
	}
}
