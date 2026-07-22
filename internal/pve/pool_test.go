package pve

import (
	"context"
	"net/http"
	"testing"
)

// TestPoolsListsVisiblePools covers the endpoint Preflight uses to prove the
// configured pool exists. PVE returns only the pools the token can audit, which
// is exactly the set a pool-scoped token is allowed to work in.
func TestPoolsListsVisiblePools(t *testing.T) {
	var gotPath string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"poolid":"sandbar","comment":"sand VMs"},{"poolid":"other"}]}`))
	})

	pools, err := c.Pools(context.Background())
	if err != nil {
		t.Fatalf("Pools: %v", err)
	}
	if gotPath != "/api2/json/pools" {
		t.Errorf("requested path = %q; want /api2/json/pools", gotPath)
	}
	if len(pools) != 2 || pools[0].PoolID != "sandbar" || pools[0].Comment != "sand VMs" {
		t.Fatalf("Pools() = %+v; want the two decoded pools", pools)
	}
}

// TestPoolsEmptyWhenTokenSeesNone confirms an empty list decodes as an empty
// slice and not an error: a token with no Pool.Audit anywhere is a
// configuration problem for the CALLER to name, not a transport failure.
func TestPoolsEmptyWhenTokenSeesNone(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	})

	pools, err := c.Pools(context.Background())
	if err != nil {
		t.Fatalf("Pools: %v", err)
	}
	if len(pools) != 0 {
		t.Fatalf("Pools() = %+v; want an empty slice", pools)
	}
}
