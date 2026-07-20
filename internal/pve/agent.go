package pve

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
)

// agentResult unwraps the DOUBLE-nested response shape every qemu-guest-agent
// passthrough endpoint uses: {"data": {"result": ...}}. The outer "data"
// envelope is unwrapped by Client.do as usual; this type unwraps the inner
// "result" key.
type agentResult[T any] struct {
	Result T `json:"result"`
}

// agentPost issues an agent command that PVE exposes as POST — ping and
// fsfreeze-status.
func (c *Client) agentPost(ctx context.Context, vmid int, cmd string, body url.Values, out any) error {
	path := fmt.Sprintf("/nodes/%s/qemu/%d/agent/%s", c.node, vmid, cmd)
	return c.do(ctx, http.MethodPost, path, nil, body, out)
}

// agentGet issues an agent command that PVE exposes as GET —
// network-get-interfaces, get-fsinfo, exec-status. The method split between
// commands is not guessable from the command name and must not be
// "normalized" to POST for consistency.
func (c *Client) agentGet(ctx context.Context, vmid int, cmd string, query url.Values, out any) error {
	path := fmt.Sprintf("/nodes/%s/qemu/%d/agent/%s", c.node, vmid, cmd)
	return c.do(ctx, http.MethodGet, path, query, nil, out)
}

// AgentPing checks guest-agent reachability via POST .../agent/ping. A nil
// error means the agent responded.
func (c *Client) AgentPing(ctx context.Context, vmid int) error {
	return c.agentPost(ctx, vmid, "ping", nil, nil)
}

// AgentFsfreezeStatus reads the guest filesystem freeze state via POST
// .../agent/fsfreeze-status (one of the two POST-exposed agent commands,
// alongside ping — despite being a read).
func (c *Client) AgentFsfreezeStatus(ctx context.Context, vmid int) (string, error) {
	var res agentResult[string]
	if err := c.agentPost(ctx, vmid, "fsfreeze-status", nil, &res); err != nil {
		return "", err
	}
	return res.Result, nil
}

// IPAddress is one address entry of a NetworkInterface. Field names are
// hyphenated in the guest-agent's own JSON (not PVE's usual convention) and
// are mapped here accordingly.
type IPAddress struct {
	IPAddress     string `json:"ip-address"`
	IPAddressType string `json:"ip-address-type"`
	Prefix        int    `json:"prefix"`
}

// NetworkInterface is one entry of AgentNetworkGetInterfaces's result.
type NetworkInterface struct {
	Name            string      `json:"name"`
	HardwareAddress string      `json:"hardware-address"`
	IPAddresses     []IPAddress `json:"ip-addresses"`
}

// AgentNetworkGetInterfaces lists guest network interfaces via GET
// .../agent/network-get-interfaces (a GET, unlike ping/fsfreeze-status).
func (c *Client) AgentNetworkGetInterfaces(ctx context.Context, vmid int) ([]NetworkInterface, error) {
	var res agentResult[[]NetworkInterface]
	if err := c.agentGet(ctx, vmid, "network-get-interfaces", nil, &res); err != nil {
		return nil, err
	}
	return res.Result, nil
}

// FSInfo is one entry of AgentGetFsinfo's result.
type FSInfo struct {
	Name       string `json:"name"`
	Mountpoint string `json:"mountpoint"`
	Type       string `json:"type"`
	UsedBytes  int64  `json:"used-bytes"`
	TotalBytes int64  `json:"total-bytes"`
}

// AgentGetFsinfo lists guest filesystems via GET .../agent/get-fsinfo (a GET,
// unlike ping/fsfreeze-status).
func (c *Client) AgentGetFsinfo(ctx context.Context, vmid int) ([]FSInfo, error) {
	var res agentResult[[]FSInfo]
	if err := c.agentGet(ctx, vmid, "get-fsinfo", nil, &res); err != nil {
		return nil, err
	}
	return res.Result, nil
}

// agentExecResult is the result shape of POST .../agent/exec.
type agentExecResult struct {
	PID int `json:"pid"`
}

// AgentExec starts command in the guest via POST .../agent/exec, returning
// the guest-side PID to be polled with AgentExecStatus. Each element of
// command is sent as a separate "command" form value (PVE's array-parameter
// convention for argv).
func (c *Client) AgentExec(ctx context.Context, vmid int, command []string, inputData string) (int, error) {
	form := url.Values{}
	for _, arg := range command {
		form.Add("command", arg)
	}
	if inputData != "" {
		form.Set("input-data", inputData)
	}
	var res agentResult[agentExecResult]
	if err := c.agentPost(ctx, vmid, "exec", form, &res); err != nil {
		return 0, err
	}
	return res.Result.PID, nil
}

// AgentExecStatus is the result shape of GET .../agent/exec-status.
type AgentExecStatus struct {
	Exited       bool   `json:"exited"`
	ExitCode     int    `json:"exitcode"`
	Signal       int    `json:"signal"`
	OutData      string `json:"out-data"`
	ErrData      string `json:"err-data"`
	OutTruncated bool   `json:"out-truncated"`
	ErrTruncated bool   `json:"err-truncated"`
}

// AgentExecStatus polls the status of a command started with AgentExec via
// GET .../agent/exec-status (a GET, unlike ping/fsfreeze-status).
func (c *Client) AgentExecStatus(ctx context.Context, vmid, pid int) (AgentExecStatus, error) {
	var res agentResult[AgentExecStatus]
	query := url.Values{"pid": {strconv.Itoa(pid)}}
	if err := c.agentGet(ctx, vmid, "exec-status", query, &res); err != nil {
		return AgentExecStatus{}, err
	}
	return res.Result, nil
}

// All four agent failure conditions below return HTTP 500 and are
// distinguishable only by message text.
var (
	agentNotConfiguredRE = regexp.MustCompile(`(?i)no qemu guest agent configured`)
	agentTimeoutRE       = regexp.MustCompile(`(?i)timeout`)
	agentDownRE          = regexp.MustCompile(`(?i)guest agent is not running|qmp command .* failed`)
	agentVMStoppedRE     = regexp.MustCompile(`(?i)is not running`)
)

// AgentUnavailableReason classifies an error returned by an Agent* call into
// "not-configured", "vm-stopped", "agent-down", "timeout", or "" (not one of
// these). It is best-effort: PVE checks `!defined($conf->{agent})` (yielding
// "not-configured") BEFORE checking whether the VM is running, so a VM
// explicitly configured with "agent: 0" has $conf->{agent} defined and skips
// that check — it instead reports "is not running" once stopped, identically
// to a properly-enabled agent whose VM is merely stopped. Message text alone
// cannot tell "agent: 0, VM stopped" apart from "agent: 1, VM stopped"; a
// caller that must distinguish "will never work" from "try again once
// booted" needs to separately read GetConfig's "agent" field.
func AgentUnavailableReason(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	switch {
	case agentNotConfiguredRE.MatchString(msg):
		return "not-configured"
	case agentTimeoutRE.MatchString(msg):
		return "timeout"
	case agentDownRE.MatchString(msg):
		return "agent-down"
	case agentVMStoppedRE.MatchString(msg):
		return "vm-stopped"
	default:
		return ""
	}
}
