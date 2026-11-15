package store

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"sync"
)

// slot is one partition in the map. While Adding is set, Adding receives writes and is
// being filled from the replicas; the reconciler then swaps Removing for Adding.
type slot struct {
	Replicas []string `json:"replicas"`
	Adding   string   `json:"adding,omitempty"`
	Removing string   `json:"removing,omitempty"`
}

// partitionMap is the durable map (ConfigMap store-partition-map, D-COORD). Incarnations
// records the unit_meta.incarnation of each unit whose copies are complete; a unit whose
// live incarnation differs (wiped disk) or is missing (new unit) serves no reads.
type partitionMap struct {
	Partitions   []slot            `json:"partitions"`
	Incarnations map[string]string `json:"incarnations"`
}

// defaultMap places p on names[p%n], names[(p+1)%n]: each of 3 units holds 2/3 (P1.1).
func defaultMap(names []string) partitionMap {
	m := partitionMap{Partitions: make([]slot, Partitions), Incarnations: map[string]string{}}
	for p := range m.Partitions {
		for i := range min(ReplicationFactor, len(names)) {
			m.Partitions[p].Replicas = append(m.Partitions[p].Replicas, names[(p+i)%len(names)])
		}
	}
	return m
}

func (m partitionMap) clone() partitionMap {
	c := partitionMap{Partitions: make([]slot, len(m.Partitions)), Incarnations: map[string]string{}}
	for p, s := range m.Partitions {
		c.Partitions[p] = slot{Replicas: slices.Clone(s.Replicas), Adding: s.Adding, Removing: s.Removing}
	}
	for k, v := range m.Incarnations {
		c.Incarnations[k] = v
	}
	return c
}

// load counts the slots each unit holds or is receiving, for picking move targets.
func (m partitionMap) load() map[string]int {
	load := map[string]int{}
	for _, s := range m.Partitions {
		for _, name := range s.Replicas {
			load[name]++
		}
		if s.Adding != "" {
			load[s.Adding]++
		}
	}
	return load
}

// layout is an immutable snapshot of the map resolved to units; every call takes one.
type layout struct {
	m        partitionMap
	replicas [Partitions][]*unit // serve reads; index 0 is the primary
	writes   [Partitions][]*unit // replicas plus Adding
}

func (r *Router) resolve(m partitionMap) *layout {
	l := &layout{m: m}
	for p, s := range m.Partitions {
		for _, name := range s.Replicas {
			if u := r.byName[name]; u != nil {
				l.replicas[p] = append(l.replicas[p], u)
			}
		}
		l.writes[p] = slices.Clone(l.replicas[p])
		if u := r.byName[s.Adding]; u != nil {
			l.writes[p] = append(l.writes[p], u)
		}
	}
	return l
}

func (r *Router) current() *layout { return r.layout.Load() }

// errMapConflict means another writer changed the map since it was loaded.
var errMapConflict = errors.New("store: partition map changed concurrently")

// mapStore persists the partition map with compare-and-swap on version.
type mapStore interface {
	// load returns found=false when no map has been saved yet.
	load(ctx context.Context) (m partitionMap, version string, found bool, err error)
	// save writes m if the stored version is still version ("" creates); it returns the new version.
	save(ctx context.Context, m partitionMap, version string) (string, error)
}

// memoryMaps serves Compose and tests, where there is no Kubernetes API.
type memoryMaps struct {
	mu      sync.Mutex
	m       *partitionMap
	version int
}

func (s *memoryMaps) load(context.Context) (partitionMap, string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		return partitionMap{}, "", false, nil
	}
	return s.m.clone(), strconv.Itoa(s.version), true, nil
}

func (s *memoryMaps) save(_ context.Context, m partitionMap, version string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if (s.m == nil) != (version == "") || (s.m != nil && version != strconv.Itoa(s.version)) {
		return "", errMapConflict
	}
	c := m.clone()
	s.m = &c
	s.version++
	return strconv.Itoa(s.version), nil
}
