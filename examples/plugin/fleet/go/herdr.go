// herdr.go: the live leadBackend over the herdr CLI. Feasibility evidence
// (docs/cpa-router.md, vraj-ai/fleet#9) fixes the callable contract:
//   - `herdr agent list` emits a JSON envelope; agents carry pane_id, the
//     agent kind name, and agent_status.
//   - `herdr agent prompt <pane> <text> --wait --until idle --until done
//     --until blocked --timeout <ms>` submits and bounds the wait; error
//     codes agent_blocked / agent_prompt_stalled / timeout are documented.
//     It does not track turns, so a busy agent's unrelated completion could
//     match — the state precheck plus the result marker close that hole.
//   - `herdr agent read <pane> --source recent-unwrapped --lines N` prints
//     raw terminal text (not JSON) for result extraction.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

type herdrAgent struct {
	Name   string `json:"agent"`
	Status string `json:"agent_status"`
	PaneID string `json:"pane_id"`
	Cwd    string `json:"cwd"`
}

type commandRunner func(ctx context.Context, argv ...string) ([]byte, error)

type cliHerdr struct {
	bin string
	run commandRunner
}

func defaultHerdr() *cliHerdr {
	return &cliHerdr{bin: resolveBin("herdr"), run: execRunner}
}

// resolveBin locates a CLI the daemon's minimal launchd PATH may not expose.
func resolveBin(name string) string {
	if path, err := exec.LookPath(name); err == nil {
		return path
	}
	for _, dir := range []string{"/opt/homebrew/bin", "/usr/local/bin"} {
		if path := filepath.Join(dir, name); fileExists(path) {
			return path
		}
	}
	return name
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func execRunner(ctx context.Context, argv ...string) ([]byte, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("empty command")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	return cmd.CombinedOutput()
}

type herdrErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// herdrEnvelope matches the CLI's stdout envelope: result on success, error
// on failure. `agent read` is the exception — it prints raw terminal text,
// and only errors arrive as this envelope.
type herdrEnvelope struct {
	Result json.RawMessage `json:"result"`
	Error  *herdrErrorBody `json:"error"`
}

func parseHerdrEnvelope(out []byte) (json.RawMessage, *routerError) {
	var env herdrEnvelope
	if err := json.Unmarshal(out, &env); err != nil {
		return nil, routerErr("herdr_unexpected_output", "herdr output was not a JSON envelope", http.StatusBadGateway)
	}
	if env.Error != nil {
		return nil, routerErr("herdr_"+env.Error.Code, env.Error.Message, http.StatusBadGateway)
	}
	return env.Result, nil
}

func (h *cliHerdr) agents(ctx context.Context) ([]herdrAgent, error) {
	out, err := h.run(ctx, h.bin, "agent", "list")
	if err != nil {
		return nil, routerErr("herdr_unavailable", fmt.Sprintf("herdr agent list failed: %v", err), http.StatusServiceUnavailable)
	}
	result, rerr := parseHerdrEnvelope(out)
	if rerr != nil {
		return nil, rerr
	}
	var parsed struct {
		Agents []herdrAgent `json:"agents"`
	}
	if err := json.Unmarshal(result, &parsed); err != nil {
		return nil, routerErr("herdr_unexpected_output", "agent list result did not parse", http.StatusBadGateway)
	}
	return parsed.Agents, nil
}

func (h *cliHerdr) prompt(ctx context.Context, pane, text string, timeoutMS int) *routerError {
	out, err := h.run(ctx, h.bin, "agent", "prompt", pane, text,
		"--wait", "--until", "idle", "--until", "done", "--until", "blocked",
		"--timeout", strconv.Itoa(timeoutMS))
	if err != nil && len(out) == 0 {
		return routerErr("herdr_unavailable", fmt.Sprintf("herdr agent prompt failed: %v", err), http.StatusServiceUnavailable)
	}
	var env herdrEnvelope
	if json.Unmarshal(out, &env) != nil {
		return routerErr("herdr_unexpected_output", "agent prompt output was not a JSON envelope", http.StatusBadGateway)
	}
	if env.Error == nil {
		return nil
	}
	switch env.Error.Code {
	case "agent_blocked":
		return routerErr("lead_blocked", "lead agent is blocked on an approval or question", http.StatusConflict)
	case "agent_prompt_stalled":
		return routerErr("lead_stalled", "lead agent did not start working within the submit window", http.StatusServiceUnavailable)
	case "timeout":
		e := routerErr("outcome_uncertain", "lead turn exceeded the wait bound; outcome unknown, not retried", http.StatusGatewayTimeout)
		e.retryable = false
		return e
	default:
		return routerErr("herdr_"+env.Error.Code, env.Error.Message, http.StatusBadGateway)
	}
}

func (h *cliHerdr) read(ctx context.Context, pane string, lines int) (string, *routerError) {
	out, err := h.run(ctx, h.bin, "agent", "read", pane, "--source", "recent-unwrapped", "--lines", strconv.Itoa(lines))
	if err != nil && len(out) == 0 {
		return "", routerErr("herdr_unavailable", fmt.Sprintf("herdr agent read failed: %v", err), http.StatusServiceUnavailable)
	}
	var env herdrEnvelope
	if json.Unmarshal(out, &env) == nil && env.Error != nil {
		return "", routerErr("herdr_"+env.Error.Code, env.Error.Message, http.StatusBadGateway)
	}
	return string(out), nil
}

// runLead implements the verified lifecycle: state precheck, correlated
// prompt with a bounded wait, read-back, marker verification. Anything less
// cannot prove the returned text belongs to this request.
func (h *cliHerdr) runLead(ctx context.Context, req leadRequest) (leadResult, error) {
	agents, err := h.agents(ctx)
	if err != nil {
		return leadResult{}, err
	}
	var target *herdrAgent
	for i := range agents {
		if agents[i].PaneID == req.Agent || agents[i].Name == req.Agent {
			target = &agents[i]
			break
		}
	}
	if target == nil {
		return leadResult{}, routerErr("lead_unavailable", fmt.Sprintf("no herdr agent matches selector %q", req.Agent), http.StatusServiceUnavailable)
	}
	switch target.Status {
	case "working", "blocked":
		return leadResult{}, routerErr("lead_busy", fmt.Sprintf("lead agent %s is %s", target.PaneID, target.Status), http.StatusConflict)
	}
	timeoutMS := int(req.Deadline / time.Millisecond)
	if timeoutMS <= 0 {
		timeoutMS = defaultRouterTimeoutMS
	}
	roleNote := ""
	switch req.Role {
	case roleSidekick:
		roleNote = " You are a READ-ONLY reviewer: do not modify files, run mutating commands, or change any state."
	case roleFinalize:
		roleNote = " You retain ownership: incorporate valid findings, reject invalid ones."
	}
	prompt := fmt.Sprintf("[cpa-router %s | role: %s | effort: %s]%s Answer as plain text. Wrap the complete final answer between %s%s and %s%s, each marker on its own line. End the framed answer with a line %sok or %sfailed.\n\n%s",
		req.CorrelationID, req.Role, req.Effort, roleNote, resultMarkerBegin, req.CorrelationID, resultMarkerEnd, req.CorrelationID, verifyMarker, verifyMarker, req.Task)
	if perr := h.prompt(ctx, target.PaneID, prompt, timeoutMS); perr != nil {
		return leadResult{}, perr
	}
	text, rerr := h.read(ctx, target.PaneID, defaultRouterReadLines)
	if rerr != nil {
		return leadResult{}, rerr
	}
	answer, ok := extractMarked(text, req.CorrelationID)
	if !ok {
		return leadResult{}, routerErr("lead_result_unverified", "lead output did not contain the correlation markers", http.StatusBadGateway)
	}
	answer, verification := splitVerification(answer)
	answer, calls := splitToolCalls(answer, req.CorrelationID)
	return leadResult{Text: answer, Agent: target.PaneID, Verification: verification, ToolCalls: calls}, nil
}

var _ leadBackend = (*cliHerdr)(nil)
