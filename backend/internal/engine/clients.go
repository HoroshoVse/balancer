package engine

import (
	"bytes"
	"encoding/binary"
	"net/netip"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// A flood from spoofed sources can be a million distinct addresses a minute, so
// the per-client statistics cannot keep one counter per address. clientTracker
// keeps the busiest addresses only, with the "space-saving" algorithm: a fixed
// table per time slice; an unknown address replaces the one with the fewest
// hits and inherits its count, so any address that sends more than 1/capacity of
// the traffic is always in the table. Memory is fixed whatever the sources are.
//
// What is reported is a lower bound: the hits counted since the address entered
// the table. For a steady client that is the exact figure; for one that was
// pushed out and came back during a flood it can be a little short.
//
// The window slides in one-minute slices, each with a table of its own; a read
// adds up the slices still inside the window.

const (
	clientShards    = 8  // addresses are spread over shards so concurrent readers rarely meet on a lock
	clientShardCap  = 32 // addresses per shard and slice: 256 tracked
	clientSlices    = 10 // slices in the window
	clientSliceSecs = 60 // length of a slice

	// ClientWindow is how far back the statistics look.
	ClientWindow = clientSlices * clientSliceSecs * time.Second

	topClientsShown = 10
)

// ipKey is an address in 16-byte form (IPv4 as IPv4-mapped), comparable and
// cheap to hash and to build, unlike netip.Addr that carries a zone pointer.
type ipKey [16]byte

func (k ipKey) addr() netip.Addr { return netip.AddrFrom16(k).Unmap() }

// shard picks the shard of an address; the finalizer of MurmurHash3 evens out
// addresses that only differ in a few low bits.
func (k ipKey) shard() int {
	h := binary.BigEndian.Uint64(k[8:]) ^ binary.BigEndian.Uint64(k[:8])*0x9e3779b97f4a7c15
	h ^= h >> 33
	h *= 0xff51afd7ed558ccd
	h ^= h >> 33
	return int(h & (clientShards - 1))
}

type clientEntry struct {
	key      ipKey
	accepted uint32 // hits served since the address entered the table
	refused  uint32
	over     uint32 // count inherited from the address it replaced
	total    uint32 // accepted + refused + over: what the table orders by
	pos      int32  // place in the heap
}

// summary is one space-saving table: a min-heap over total, so the least busy
// address is always at the top and replacing it costs O(log capacity).
type summary struct {
	ents []clientEntry
	heap []int32 // indexes into ents
	idx  map[ipKey]int32
	// labels says what each key stands for, by entry index, for trackers whose
	// keys are hashes (see domainTracker); nil for addresses, which say it themselves
	labels []string
}

func (s *summary) less(a, b int) bool { return s.ents[s.heap[a]].total < s.ents[s.heap[b]].total }

func (s *summary) swap(a, b int) {
	s.heap[a], s.heap[b] = s.heap[b], s.heap[a]
	s.ents[s.heap[a]].pos = int32(a)
	s.ents[s.heap[b]].pos = int32(b)
}

func (s *summary) siftUp(i int) {
	for i > 0 {
		p := (i - 1) / 2
		if !s.less(i, p) {
			return
		}
		s.swap(i, p)
		i = p
	}
}

func (s *summary) siftDown(i int) {
	n := len(s.heap)
	for {
		m := 2*i + 1
		if m >= n {
			return
		}
		if r := m + 1; r < n && s.less(r, m) {
			m = r
		}
		if !s.less(m, i) {
			return
		}
		s.swap(i, m)
		i = m
	}
}

// add counts one hit of k. fresh is true when k has just taken a place in the
// table (new, or replacing the least busy key), so the caller can attach to
// entry i whatever describes the key.
func (s *summary) add(k ipKey, refused bool) (i int32, fresh bool) {
	if i, ok := s.idx[k]; ok {
		e := &s.ents[i]
		if refused {
			e.refused++
		} else {
			e.accepted++
		}
		e.total++
		s.siftDown(int(e.pos)) // it only grew, so it can only move away from the top
		return i, false
	}
	if s.idx == nil { // the memory of a slice is taken when it first sees traffic
		s.idx = make(map[ipKey]int32, clientShardCap)
		s.ents = make([]clientEntry, 0, clientShardCap)
		s.heap = make([]int32, 0, clientShardCap)
	}
	if len(s.ents) < clientShardCap {
		i = int32(len(s.ents))
		e := clientEntry{key: k, total: 1, pos: int32(len(s.heap))}
		if refused {
			e.refused = 1
		} else {
			e.accepted = 1
		}
		s.ents = append(s.ents, e)
		s.heap = append(s.heap, i)
		s.idx[k] = i
		s.siftUp(int(e.pos))
		return i, true
	}
	// Full: the least busy address makes room and its count is the new one's head start
	i = s.heap[0]
	e := &s.ents[i]
	delete(s.idx, e.key)
	e.key, e.accepted, e.refused, e.over = k, 0, 0, e.total
	e.total++
	if refused {
		e.refused = 1
	} else {
		e.accepted = 1
	}
	s.idx[k] = i
	s.siftDown(0)
	return i, true
}

func (s *summary) reset() {
	s.ents, s.heap = s.ents[:0], s.heap[:0]
	clear(s.idx)
	clear(s.labels)
}

type clientShard struct {
	mu   sync.Mutex
	cur  int64 // number of the newest slice: unix time / clientSliceSecs
	ring [clientSlices]summary
}

// advance moves the window to slice n, emptying the slices that fell out of it.
func (s *clientShard) advance(n int64) {
	if n <= s.cur { // also covers a clock that stepped back
		return
	}
	if n-s.cur >= clientSlices {
		for i := range s.ring {
			s.ring[i].reset()
		}
	} else {
		for i := s.cur + 1; i <= n; i++ {
			s.ring[i%clientSlices].reset()
		}
	}
	s.cur = n
}

// tracker is the windowed space-saving table: a hit is counted against a key,
// and the busiest keys of the window can be listed. It is safe for concurrent
// use and allocates nothing per hit once the tables of the window exist.
type tracker struct {
	shards [clientShards]clientShard
	clock  func() int64 // unix seconds; replaced in tests
}

var coarse struct {
	once sync.Once
	sec  atomic.Int64
}

// coarseUnix is the time to the second, kept by one goroutine for the whole
// process: reading it costs an atomic load where time.Now costs a system clock
// read on every datagram of a flood.
func coarseUnix() int64 {
	coarse.once.Do(func() {
		coarse.sec.Store(time.Now().Unix())
		go func() {
			for now := range time.Tick(time.Second) {
				coarse.sec.Store(now.Unix())
			}
		}()
	})
	return coarse.sec.Load()
}

// add counts one hit of k. label, when given, is copied the first time k takes
// a place in a table: it is the only allocation a hit can cost.
func (t *tracker) add(k ipKey, refused bool, label []byte) {
	n := t.clock() / clientSliceSecs
	s := &t.shards[k.shard()]
	s.mu.Lock()
	s.advance(n)
	sum := &s.ring[s.cur%clientSlices]
	i, fresh := sum.add(k, refused)
	if fresh && label != nil {
		if sum.labels == nil {
			sum.labels = make([]string, clientShardCap)
		}
		sum.labels[i] = string(label)
	}
	s.mu.Unlock()
}

// tally is a key with what it was counted over the window.
type tally struct {
	key               ipKey
	label             string
	accepted, refused uint64
}

func (c tally) hits() uint64 { return c.accepted + c.refused }

// ranked returns the n busiest keys of the window, busiest first.
func (t *tracker) ranked(n int) []tally {
	if n <= 0 {
		return nil
	}
	now := t.clock() / clientSliceSecs
	sum := map[ipKey]*tally{}
	for i := range t.shards {
		s := &t.shards[i]
		s.mu.Lock()
		s.advance(now)
		for j := range s.ring {
			r := &s.ring[j]
			for idx, e := range r.ents {
				c := sum[e.key]
				if c == nil {
					c = &tally{key: e.key}
					sum[e.key] = c
				}
				if r.labels != nil && c.label == "" {
					c.label = r.labels[idx]
				}
				c.accepted += uint64(e.accepted)
				c.refused += uint64(e.refused)
			}
		}
		s.mu.Unlock()
	}
	out := make([]tally, 0, len(sum))
	for _, c := range sum {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].hits(), out[j].hits()
		return a > b || a == b && bytes.Compare(out[i].key[:], out[j].key[:]) < 0
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// size is how many keys are held right now (tests check it stays bounded).
func (t *tracker) size() int {
	n := 0
	for i := range t.shards {
		s := &t.shards[i]
		s.mu.Lock()
		for j := range s.ring {
			n += len(s.ring[j].ents)
		}
		s.mu.Unlock()
	}
	return n
}

// clientTracker counts the hits of every client address, served or refused.
type clientTracker struct{ tracker }

func newClientTracker() *clientTracker { return &clientTracker{tracker{clock: coarseUnix}} }

// record counts one connection or datagram from a.
func (t *clientTracker) record(a netip.Addr, refused bool) {
	if t == nil {
		return
	}
	t.add(ipKey(a.As16()), refused, nil)
}

type clientCount struct {
	addr              netip.Addr
	accepted, refused uint64
}

// top returns the n busiest addresses of the window, busiest first.
func (t *clientTracker) top(n int) []clientCount {
	if t == nil {
		return nil
	}
	ranked := t.ranked(n)
	out := make([]clientCount, len(ranked))
	for i, c := range ranked {
		out[i] = clientCount{c.key.addr(), c.accepted, c.refused}
	}
	return out
}

// ClientStat is one row of "who is hitting this balancer".
type ClientStat struct {
	IP       string `json:"ip"`
	Country  string `json:"country"`            // "" when unknown or the database is not loaded
	Internal bool   `json:"internal,omitempty"` // a private, loopback or link-local address
	Accepted uint64 `json:"accepted"`
	Refused  uint64 `json:"refused"`
}

// topClients lists the busiest addresses with their countries. Countries are
// looked up here, for the few rows returned, never per datagram.
func (t *clientTracker) topClients(n int) []ClientStat {
	top := t.top(n)
	if len(top) == 0 {
		return nil
	}
	out := make([]ClientStat, len(top))
	for i, c := range top {
		out[i] = ClientStat{IP: c.addr.String(), Accepted: c.accepted, Refused: c.refused, Internal: isInternal(c.addr)}
		if GeoIP != nil && !out[i].Internal {
			if cc, loaded := GeoIP.Country(c.addr); loaded {
				out[i].Country = cc
			}
		}
	}
	return out
}
