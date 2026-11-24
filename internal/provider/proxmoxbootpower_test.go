package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/vm"
)

// bootPowerMock serves a VM "web" (VMID 100) in the given power state whose
// config carries a provenance block built from cfg and the given onboot value.
// It records every onboot written to the config.
func bootPowerMock(t *testing.T, status string, cfg vm.CreateConfig, onboot string) (*pveMock, *[]string) {
	t.Helper()
	m := newPVEMock(t)
	m.data("/cluster/resources", clusterResources)
	m.data("/nodes/pve1/qemu/100/status/current", fmt.Sprintf(`{"vmid":100,"name":"web","status":%q}`, status))
	for _, verb := range []string{"start", "shutdown"} {
		m.on("/nodes/pve1/qemu/100/status/"+verb, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"data":%q}`, testUPID)
		})
	}
	m.okTask(testUPID)
	m.data("/nodes/pve1/qemu/100/agent/ping", `null`)
	m.data("/nodes/pve1/qemu/100/agent/network-get-interfaces", agentInterfaces)

	payload, err := json.Marshal(NewProvenance(cfg, false))
	if err != nil {
		t.Fatal(err)
	}
	conf := map[string]any{
		"name":        "web",
		"net0":        "virtio=BC:24:11:AA:BB:CC,bridge=vmbr0",
		"description": spliceDescriptionBlock("operator notes", payload),
	}
	if onboot != "" {
		conf["onboot"] = json.Number(onboot)
	}
	confJSON, err := json.Marshal(conf)
	if err != nil {
		t.Fatal(err)
	}
	var writes []string
	m.on("/nodes/pve1/qemu/100/config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPut {
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			if len(r.PostForm) != 1 {
				t.Errorf("boot flag write carried other fields: %v", r.PostForm)
			}
			writes = append(writes, r.PostForm.Get("onboot"))
			_, _ = w.Write([]byte(`{"data":null}`))
			return
		}
		_, _ = fmt.Fprintf(w, `{"data":%s}`, confJSON)
	})
	return m, &writes
}

func followConfig(follow, disabled bool) vm.CreateConfig {
	cfg := vm.DefaultCreateConfig()
	cfg.Name = "web"
	cfg.BootFollowsPower = follow
	cfg.DisableStartAtBoot = disabled
	return cfg
}

// A VM set to follow its power state gets onboot=1 when sandbar starts it and
// onboot=0 when sandbar stops it, so a host reboot restores what was running.
func TestProxmoxBootFollowsPower(t *testing.T) {
	shortAgentPolling(t, 5*time.Second)
	for _, tc := range []struct {
		name, status, onboot string
		start                bool
		want                 string
	}{
		{"start sets onboot", "stopped", "0", true, "1"},
		{"start with absent onboot", "stopped", "", true, "1"},
		{"stop clears onboot", "running", "1", false, "0"},
		{"stop of an already stopped VM still clears it", "stopped", "1", false, "0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, writes := bootPowerMock(t, tc.status, followConfig(true, false), tc.onboot)
			p := newProxmoxForTest(t, m)
			if _, err := p.List(); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			var err error
			if tc.start {
				err = p.StartStreaming(context.Background(), "web", &out)
			} else {
				err = p.StopStreaming(context.Background(), "web", &out)
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(*writes) != 1 || (*writes)[0] != tc.want {
				t.Fatalf("onboot writes = %v, want [%s]", *writes, tc.want)
			}
			if !strings.Contains(out.String(), "when the Proxmox host boots") {
				t.Errorf("the boot flag change was not reported:\n%s", out.String())
			}
		})
	}
}

// Without the setting, or with start-at-boot disabled, or with the flag already
// right, a power verb never writes the VM's config.
func TestProxmoxBootFollowsPowerLeavesFlagAlone(t *testing.T) {
	shortAgentPolling(t, 5*time.Second)
	for _, tc := range []struct {
		name             string
		follow, disabled bool
		status, onboot   string
		start            bool
	}{
		{"setting off, start", false, false, "stopped", "0", true},
		{"setting off, stop", false, false, "running", "1", false},
		{"start at boot disabled", true, true, "running", "0", false},
		{"already on", true, false, "stopped", "1", true},
		{"already off", true, false, "running", "0", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, writes := bootPowerMock(t, tc.status, followConfig(tc.follow, tc.disabled), tc.onboot)
			p := newProxmoxForTest(t, m)
			if _, err := p.List(); err != nil {
				t.Fatal(err)
			}
			var err error
			if tc.start {
				err = p.Start("web")
			} else {
				err = p.Stop("web")
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(*writes) != 0 {
				t.Fatalf("onboot writes = %v, want none", *writes)
			}
		})
	}
}

// The power change has already happened when the flag is written, so a failed
// write must not turn a successful stop into a reported failure.
func TestProxmoxBootFollowsPowerWriteFailureIsAWarning(t *testing.T) {
	m, _ := bootPowerMock(t, "running", followConfig(true, false), "1")
	m.on("/nodes/pve1/qemu/100/config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			http.Error(w, "permission denied", http.StatusForbidden)
			return
		}
		payload, _ := json.Marshal(NewProvenance(followConfig(true, false), false))
		desc, _ := json.Marshal(spliceDescriptionBlock("", payload))
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"data":{"name":"web","onboot":1,"description":%s}}`, desc)
	})
	p := newProxmoxForTest(t, m)
	if _, err := p.List(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := p.StopStreaming(context.Background(), "web", &out); err != nil {
		t.Fatalf("a failed boot flag write failed the stop: %v", err)
	}
	if !strings.Contains(out.String(), "warning: could not update web's start at boot") {
		t.Errorf("the failed write was not reported:\n%s", out.String())
	}
}

// A VMID recycled between the power call and the config read belongs to some
// other VM; its boot flag must never be touched.
func TestProxmoxBootFollowsPowerChecksName(t *testing.T) {
	m, writes := bootPowerMock(t, "running", followConfig(true, false), "1")
	payload, _ := json.Marshal(NewProvenance(followConfig(true, false), false))
	desc, _ := json.Marshal(spliceDescriptionBlock("", payload))
	m.on("/nodes/pve1/qemu/100/config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			*writes = append(*writes, "write")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"data":{"name":"someone-else","onboot":1,"description":%s}}`, desc)
	})
	p := newProxmoxForTest(t, m)
	if _, err := p.List(); err != nil {
		t.Fatal(err)
	}
	if err := p.Stop("web"); err != nil {
		t.Fatal(err)
	}
	if len(*writes) != 0 {
		t.Fatalf("wrote another VM's boot flag: %v", *writes)
	}
}
