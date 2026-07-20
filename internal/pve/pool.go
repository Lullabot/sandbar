package pve

import (
	"context"
	"net/http"
)

// Pool is one entry of GET /pools — a PVE resource pool. Only the identity and
// its comment are decoded; the member list is deliberately not, because the one
// caller (a provider's preflight) asks a membership-free question: "does the
// pool this profile is scoped to exist, and can this token see it?"
type Pool struct {
	PoolID  string `json:"poolid"`
	Comment string `json:"comment"`
}

// Pools lists the resource pools this token can see, via GET /pools.
//
// The bare collection endpoint is used rather than the per-pool
// GET /pools/{poolid}: the single-pool form is deprecated upstream in favour of
// a `?poolid=` query added in PVE 8, so preferring the collection keeps one code
// path working across the 8.x-9.x range this client supports instead of forking
// on version.
//
// PVE filters the result to pools the token holds Pool.Audit on, so an absent
// pool and an unauditable one are indistinguishable here. That ambiguity belongs
// to the caller to phrase — it must name BOTH causes rather than asserting the
// pool does not exist, which would send an operator hunting for a pool that is
// sitting right there.
func (c *Client) Pools(ctx context.Context) ([]Pool, error) {
	var pools []Pool
	if err := c.do(ctx, http.MethodGet, "/pools", nil, nil, &pools); err != nil {
		return nil, err
	}
	return pools, nil
}
