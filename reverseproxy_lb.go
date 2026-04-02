// This Source Code Form is subject to the terms of the Mozilla Public License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
// Copyright 2026 WJQSERVER. All rights reserved.
// All rights reserved by WJQSERVER, related rights can be exercised by the infinite-iroha organization.
package touka

import (
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ReverseProxyLoadBalancingConfig configures upstream selection and retries.
type ReverseProxyLoadBalancingConfig struct {
	Policy      ReverseProxyLBPolicy
	Retries     int
	TryDuration time.Duration
	TryInterval time.Duration
}

// ReverseProxyPassiveHealthConfig configures inline passive health tracking.
type ReverseProxyPassiveHealthConfig struct {
	FailDuration    time.Duration
	MaxFails        int
	UnhealthyStatus []int
}

// ReverseProxyLBPolicy selects an upstream from the configured target pool.
// Use the helper constructors such as LBRandom or LBHeader to build a policy.
type ReverseProxyLBPolicy struct {
	kind     reverseProxyLBPolicyKind
	key      string
	fallback *ReverseProxyLBPolicy
}

type reverseProxyLBPolicyKind uint8

const (
	reverseProxyLBPolicyRandom reverseProxyLBPolicyKind = iota
	reverseProxyLBPolicyRoundRobin
	reverseProxyLBPolicyFirst
	reverseProxyLBPolicyLeastConn
	reverseProxyLBPolicyIPHash
	reverseProxyLBPolicyClientIPHash
	reverseProxyLBPolicyURIHash
	reverseProxyLBPolicyHeader
	reverseProxyLBPolicyQuery
)

type reverseProxyUpstream struct {
	key                      string
	target                   *url.URL
	index                    int
	useH2C                   bool
	extendedConnectTransport http.RoundTripper
	bridgeTransport          http.RoundTripper
	h2cTransport             http.RoundTripper
	inFlight                 atomic.Int64

	passiveMu sync.Mutex
	failures  []time.Time
}

func LBRandom() ReverseProxyLBPolicy {
	return ReverseProxyLBPolicy{kind: reverseProxyLBPolicyRandom}
}

func LBRoundRobin() ReverseProxyLBPolicy {
	return ReverseProxyLBPolicy{kind: reverseProxyLBPolicyRoundRobin}
}

func LBFirst() ReverseProxyLBPolicy {
	return ReverseProxyLBPolicy{kind: reverseProxyLBPolicyFirst}
}

func LBLeastConn() ReverseProxyLBPolicy {
	return ReverseProxyLBPolicy{kind: reverseProxyLBPolicyLeastConn}
}

func LBIPHash() ReverseProxyLBPolicy {
	return ReverseProxyLBPolicy{kind: reverseProxyLBPolicyIPHash}
}

func LBClientIPHash() ReverseProxyLBPolicy {
	return ReverseProxyLBPolicy{kind: reverseProxyLBPolicyClientIPHash}
}

func LBURIHash() ReverseProxyLBPolicy {
	return ReverseProxyLBPolicy{kind: reverseProxyLBPolicyURIHash}
}

func LBHeader(field string, fallback ReverseProxyLBPolicy) ReverseProxyLBPolicy {
	policy := ReverseProxyLBPolicy{kind: reverseProxyLBPolicyHeader, key: textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(field))}
	if fallback.kind != reverseProxyLBPolicyRandom || fallback.key != "" || fallback.fallback != nil {
		policy.fallback = &fallback
	}
	return policy
}

func LBQuery(key string, fallback ReverseProxyLBPolicy) ReverseProxyLBPolicy {
	policy := ReverseProxyLBPolicy{kind: reverseProxyLBPolicyQuery, key: strings.TrimSpace(key)}
	if fallback.kind != reverseProxyLBPolicyRandom || fallback.key != "" || fallback.fallback != nil {
		policy.fallback = &fallback
	}
	return policy
}

func validateReverseProxyLBPolicy(policy ReverseProxyLBPolicy) error {
	switch policy.kind {
	case reverseProxyLBPolicyRandom, reverseProxyLBPolicyRoundRobin, reverseProxyLBPolicyFirst,
		reverseProxyLBPolicyLeastConn, reverseProxyLBPolicyIPHash, reverseProxyLBPolicyClientIPHash,
		reverseProxyLBPolicyURIHash:
		return nil
	case reverseProxyLBPolicyHeader:
		if policy.key == "" {
			return fmt.Errorf("reverse proxy header load-balancing policy requires a header field")
		}
	case reverseProxyLBPolicyQuery:
		if policy.key == "" {
			return fmt.Errorf("reverse proxy query load-balancing policy requires a query key")
		}
	default:
		return fmt.Errorf("reverse proxy load-balancing policy is invalid")
	}
	if policy.fallback != nil {
		return validateReverseProxyLBPolicy(*policy.fallback)
	}
	return nil
}

func (p *reverseProxyHandler) selectUpstream(c *Context, excluded map[string]struct{}) (*reverseProxyUpstream, error) {
	now := time.Now()
	policy := p.config.LoadBalancing.Policy
	candidates := p.availableUpstreams(now, excluded)
	if len(candidates) == 0 && len(excluded) > 0 {
		candidates = p.availableUpstreams(now, nil)
	}
	if len(candidates) == 0 {
		return nil, errReverseProxyNoAvailableUpstreams
	}
	return p.selectUpstreamWithPolicy(c, candidates, policy), nil
}

func (p *reverseProxyHandler) availableUpstreams(now time.Time, excluded map[string]struct{}) []*reverseProxyUpstream {
	candidates := make([]*reverseProxyUpstream, 0, len(p.upstreams))
	for _, upstream := range p.upstreams {
		if _, skip := excluded[upstream.key]; skip {
			continue
		}
		if !upstream.healthy(now, p.config.PassiveHealth) {
			continue
		}
		candidates = append(candidates, upstream)
	}
	return candidates
}

func (p *reverseProxyHandler) selectUpstreamWithPolicy(c *Context, candidates []*reverseProxyUpstream, policy ReverseProxyLBPolicy) *reverseProxyUpstream {
	if len(candidates) == 0 {
		return nil
	}

	switch policy.kind {
	case reverseProxyLBPolicyRoundRobin:
		return candidates[p.nextRoundRobinIndex(len(candidates))]
	case reverseProxyLBPolicyFirst:
		return candidates[0]
	case reverseProxyLBPolicyLeastConn:
		return p.selectLeastConnUpstream(candidates)
	case reverseProxyLBPolicyIPHash:
		return reverseProxySelectHRW(candidates, reverseProxyClientIP(c.Request.RemoteAddr))
	case reverseProxyLBPolicyClientIPHash:
		return reverseProxySelectHRW(candidates, c.RequestIP())
	case reverseProxyLBPolicyURIHash:
		if c.Request == nil || c.Request.URL == nil {
			return reverseProxySelectRandom(candidates)
		}
		return reverseProxySelectHRW(candidates, c.Request.URL.RequestURI())
	case reverseProxyLBPolicyHeader:
		if c.Request != nil && c.Request.Header != nil {
			if values, ok := c.Request.Header[policy.key]; ok {
				return reverseProxySelectHRW(candidates, strings.Join(values, ","))
			}
		}
		return p.selectUpstreamWithPolicy(c, candidates, reverseProxyFallbackPolicy(policy))
	case reverseProxyLBPolicyQuery:
		if c.Request != nil && c.Request.URL != nil {
			if values, ok := c.Request.URL.Query()[policy.key]; ok {
				return reverseProxySelectHRW(candidates, strings.Join(values, ","))
			}
		}
		return p.selectUpstreamWithPolicy(c, candidates, reverseProxyFallbackPolicy(policy))
	case reverseProxyLBPolicyRandom:
		fallthrough
	default:
		return reverseProxySelectRandom(candidates)
	}
}

func (p *reverseProxyHandler) nextRoundRobinIndex(size int) int {
	if size <= 1 {
		return 0
	}
	return int((p.roundRobin.Add(1) - 1) % uint64(size))
}

func (p *reverseProxyHandler) selectLeastConnUpstream(candidates []*reverseProxyUpstream) *reverseProxyUpstream {
	if len(candidates) == 0 {
		return nil
	}
	selected := candidates[0]
	lowest := selected.inFlight.Load()
	ties := []*reverseProxyUpstream{selected}
	for _, upstream := range candidates[1:] {
		count := upstream.inFlight.Load()
		switch {
		case count < lowest:
			selected = upstream
			lowest = count
			ties = []*reverseProxyUpstream{upstream}
		case count == lowest:
			ties = append(ties, upstream)
		}
	}
	if len(ties) == 1 {
		return selected
	}
	return ties[p.nextRoundRobinIndex(len(ties))]
}

func reverseProxySelectRandom(candidates []*reverseProxyUpstream) *reverseProxyUpstream {
	if len(candidates) == 0 {
		return nil
	}
	if len(candidates) == 1 {
		return candidates[0]
	}
	return candidates[rand.IntN(len(candidates))]
}

func reverseProxySelectHRW(candidates []*reverseProxyUpstream, key string) *reverseProxyUpstream {
	if len(candidates) == 0 {
		return nil
	}
	if key == "" {
		return reverseProxySelectRandom(candidates)
	}
	selected := candidates[0]
	bestScore := reverseProxyHRWScore(key, selected.key)
	for _, upstream := range candidates[1:] {
		score := reverseProxyHRWScore(key, upstream.key)
		if score > bestScore {
			selected = upstream
			bestScore = score
		}
	}
	return selected
}

func reverseProxyHRWScore(key, upstreamKey string) uint64 {
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	h := uint64(offset64)
	for i := 0; i < len(key); i++ {
		h ^= uint64(key[i])
		h *= prime64
	}
	h ^= 0xff
	h *= prime64
	for i := 0; i < len(upstreamKey); i++ {
		h ^= uint64(upstreamKey[i])
		h *= prime64
	}
	return h
}

func reverseProxyFallbackPolicy(policy ReverseProxyLBPolicy) ReverseProxyLBPolicy {
	if policy.fallback != nil {
		return *policy.fallback
	}
	return LBRandom()
}

func (u *reverseProxyUpstream) healthy(now time.Time, config ReverseProxyPassiveHealthConfig) bool {
	maxFails := reverseProxyPassiveMaxFails(config)
	if config.FailDuration <= 0 || maxFails <= 0 {
		return true
	}

	u.passiveMu.Lock()
	defer u.passiveMu.Unlock()
	u.pruneFailuresLocked(now, config.FailDuration)
	return len(u.failures) < maxFails
}

func (u *reverseProxyUpstream) recordFailure(now time.Time, config ReverseProxyPassiveHealthConfig) {
	maxFails := reverseProxyPassiveMaxFails(config)
	if config.FailDuration <= 0 || maxFails <= 0 {
		return
	}

	u.passiveMu.Lock()
	defer u.passiveMu.Unlock()
	u.pruneFailuresLocked(now, config.FailDuration)
	u.failures = append(u.failures, now)
}

func (u *reverseProxyUpstream) pruneFailuresLocked(now time.Time, window time.Duration) {
	if len(u.failures) == 0 || window <= 0 {
		if window <= 0 {
			u.failures = nil
		}
		return
	}
	cutoff := now.Add(-window)
	keep := 0
	for _, failureAt := range u.failures {
		if failureAt.Before(cutoff) {
			continue
		}
		u.failures[keep] = failureAt
		keep++
	}
	u.failures = u.failures[:keep]
}

func reverseProxyPassiveMaxFails(config ReverseProxyPassiveHealthConfig) int {
	if config.FailDuration <= 0 {
		return 0
	}
	if config.MaxFails <= 0 {
		return 1
	}
	return config.MaxFails
}

func reverseProxyStatusIsUnhealthy(config ReverseProxyPassiveHealthConfig, status int) bool {
	if status <= 0 {
		return false
	}
	for _, unhealthyStatus := range config.UnhealthyStatus {
		if status == unhealthyStatus {
			return true
		}
	}
	return false
}
