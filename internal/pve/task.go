package pve

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// UPID identifies a PVE asynchronous task. Only the fields callers actually
// need are extracted; Raw is kept because the caller must re-send the exact
// original string (percent-encoded) to poll or query the task's log.
type UPID struct {
	Raw, Node, Type, ID, User string
}

// upidFieldCount is the number of ':'-separated elements in a UPID string,
// INCLUDING the empty element produced by the format's mandatory trailing
// colon: "UPID:<node>:<pid>:<pstart>:<starttime>:<type>:<id>:<user>:".
const upidFieldCount = 9

// ParseUPID parses a PVE UPID string. pstart (parts[3]) is deliberately not
// validated as exactly 8 hex digits: it is 9 on a host with uptime over 497
// days, and this client only round-trips the field, never interprets it.
func ParseUPID(s string) (UPID, error) {
	parts := strings.Split(s, ":")
	if len(parts) != upidFieldCount || parts[0] != "UPID" {
		return UPID{}, fmt.Errorf("pve: malformed UPID %q", s)
	}
	return UPID{
		Raw:  s,
		Node: parts[1],
		Type: parts[5],
		ID:   parts[6],
		User: parts[7],
	}, nil
}

// warnRE matches the ONE non-"OK" exitstatus PVE treats as success.
var warnRE = regexp.MustCompile(`^WARNINGS: \d+$`)

// taskSucceeded reports whether a finished task's exitstatus means success.
// Proxmox treats "WARNINGS: n" as SUCCESS; classifying it as failure is a real
// bug class that produces duplicate VMs (the caller creates the VM, decides the
// task failed, drops it from state, and creates another on the next run).
func taskSucceeded(exitStatus string) bool {
	return exitStatus == "OK" || warnRE.MatchString(exitStatus)
}

// waitTaskPollInterval is WaitTask's initial delay between polls, backing off
// linearly up to waitTaskMaxPollInterval. Both are vars, not consts, so tests
// can shrink them and keep the suite fast instead of waiting on real seconds.
var (
	waitTaskPollInterval    = time.Second
	waitTaskMaxPollInterval = 5 * time.Second
)

// taskStatusResponse is the JSON shape of GET /nodes/{node}/tasks/{upid}/status.
type taskStatusResponse struct {
	Status     string `json:"status"`     // "running" or "stopped"
	ExitStatus string `json:"exitstatus"` // meaningful only once Status == "stopped"
}

// taskLogLine is one entry of GET /nodes/{node}/tasks/{upid}/log.
type taskLogLine struct {
	T string `json:"t"`
}

// WaitTask polls a task to completion, honouring ctx cancellation, and
// classifies its outcome using taskSucceeded — so "WARNINGS: n" is reported as
// success, not failure.
//
// Three response shapes are tolerated as TRANSIENT and cause a retry rather
// than an error: an HTTP 400 whose body's "errors" map has an "upid" key (the
// task is dispatched but not yet visible to the status endpoint — matched on
// the key, never the status code, because a genuinely malformed request also
// reports 400 with an indistinguishable code), and HTTP 596/599 (proxy
// artifacts seen in front of some PVE deployments). A permission error
// (401/403) is the opposite: it is returned immediately and never retried, so
// a mis-scoped token fails fast instead of polling forever.
func (c *Client) WaitTask(ctx context.Context, upid string) error {
	path := fmt.Sprintf("/nodes/%s/tasks/%s/status", c.node, url.PathEscape(upid))
	delay := waitTaskPollInterval

	for {
		var st taskStatusResponse
		err := c.do(ctx, http.MethodGet, path, nil, nil, &st)

		switch {
		case err == nil && st.Status != "running" && st.Status != "":
			if taskSucceeded(st.ExitStatus) {
				return nil
			}
			return c.taskFailedErr(ctx, upid, st.ExitStatus)

		case err == nil:
			// Still running — OR a transient empty/null {"data":...} envelope,
			// which do() decodes to a zero-valued Status. Treating that empty
			// status as a finished task would abort a live start/create/delete
			// with a spurious `exitstatus ""`, so it keeps polling too (bounded by
			// ctx). Fall through to the poll delay below.

		case IsPermission(err):
			return err // permanent: never retry

		default:
			var ae *APIError
			transient := errors.As(err, &ae) &&
				((ae.Status == http.StatusBadRequest && ae.HasErrorKey("upid")) ||
					ae.Status == 596 || ae.Status == 599)
			if !transient {
				return err
			}
			// else: task not yet registered, or a proxy artifact — keep polling.
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		if delay += waitTaskPollInterval; delay > waitTaskMaxPollInterval {
			delay = waitTaskMaxPollInterval
		}
	}
}

// taskFailedErr builds the error WaitTask returns for a task that finished as
// anything other than success, best-effort appending the tail of its log.
// The log request asks for a high explicit limit because the endpoint's
// default of 50 lines typically truncates BEFORE the "TASK ERROR:" line that
// usually names the actual cause.
func (c *Client) taskFailedErr(ctx context.Context, upid, exitStatus string) error {
	base := fmt.Errorf("pve: task %s failed: exitstatus %q", upid, exitStatus)

	var lines []taskLogLine
	path := fmt.Sprintf("/nodes/%s/tasks/%s/log", c.node, url.PathEscape(upid))
	if err := c.do(ctx, http.MethodGet, path, url.Values{"limit": {"1000"}}, nil, &lines); err != nil || len(lines) == 0 {
		return base // log unavailable: the base error alone is still useful
	}

	tail := lines
	const maxTailLines = 5
	if len(tail) > maxTailLines {
		tail = tail[len(tail)-maxTailLines:]
	}
	text := make([]string, len(tail))
	for i, l := range tail {
		text[i] = l.T
	}
	return fmt.Errorf("%w\n%s", base, strings.Join(text, "\n"))
}
