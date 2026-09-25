package whatsmeow_service

import (
	"container/list"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

const maxCachedLIDMappings = 4096

var _ store.LIDStore = (*boundedLIDStore)(nil)

type lidPair struct {
	pn  string
	lid string
}

type lidPairCache struct {
	mu       sync.Mutex
	capacity int
	order    *list.List
	byPN     map[string]*list.Element
	byLID    map[string]*list.Element
}

func newLIDPairCache(capacity int) *lidPairCache {
	if capacity < 1 {
		capacity = 1
	}
	return &lidPairCache{
		capacity: capacity,
		order:    list.New(),
		byPN:     make(map[string]*list.Element, capacity),
		byLID:    make(map[string]*list.Element, capacity),
	}
}

func (c *lidPairCache) getByPN(pn string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.byPN[pn]
	if !ok {
		return "", false
	}
	c.order.MoveToFront(element)
	return element.Value.(lidPair).lid, true
}

func (c *lidPairCache) getByLID(lid string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.byLID[lid]
	if !ok {
		return "", false
	}
	c.order.MoveToFront(element)
	return element.Value.(lidPair).pn, true
}

func (c *lidPairCache) put(pn, lid string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.byPN[pn]; ok {
		c.remove(old)
	}
	if lid != "" {
		if old, ok := c.byLID[lid]; ok {
			c.remove(old)
		}
	}

	element := c.order.PushFront(lidPair{pn: pn, lid: lid})
	c.byPN[pn] = element
	if lid != "" {
		c.byLID[lid] = element
	}
	if c.order.Len() > c.capacity {
		c.remove(c.order.Back())
	}
}

func (c *lidPairCache) remove(element *list.Element) {
	if element == nil {
		return
	}
	pair := element.Value.(lidPair)
	if c.byPN[pair.pn] == element {
		delete(c.byPN, pair.pn)
	}
	if pair.lid != "" && c.byLID[pair.lid] == element {
		delete(c.byLID, pair.lid)
	}
	c.order.Remove(element)
}

// boundedLIDStore replaces whatsmeow's unbounded in-memory LID mapping maps.
// Mappings remain in Postgres; cache eviction only causes a database lookup.
type boundedLIDStore struct {
	db    *sql.DB
	cache *lidPairCache
}

func newBoundedLIDStore(db *sql.DB) *boundedLIDStore {
	return &boundedLIDStore{db: db, cache: newLIDPairCache(maxCachedLIDMappings)}
}

func (s *boundedLIDStore) GetLIDForPN(ctx context.Context, pn types.JID) (types.JID, error) {
	if pn.Server != types.DefaultUserServer {
		return types.JID{}, fmt.Errorf("invalid GetLIDForPN call with non-PN JID %s", pn)
	}
	if lid, ok := s.cache.getByPN(pn.User); ok {
		if lid == "" {
			return types.JID{}, nil
		}
		return types.JID{User: lid, Device: pn.Device, Server: types.HiddenUserServer}, nil
	}

	var lid string
	err := s.db.QueryRowContext(ctx, "SELECT lid FROM whatsmeow_lid_map WHERE pn=$1", pn.User).Scan(&lid)
	if errors.Is(err, sql.ErrNoRows) {
		s.cache.put(pn.User, "")
		return types.JID{}, nil
	}
	if err != nil {
		return types.JID{}, err
	}
	s.cache.put(pn.User, lid)
	return types.JID{User: lid, Device: pn.Device, Server: types.HiddenUserServer}, nil
}

func (s *boundedLIDStore) GetPNForLID(ctx context.Context, lid types.JID) (types.JID, error) {
	if lid.Server != types.HiddenUserServer {
		return types.JID{}, fmt.Errorf("invalid GetPNForLID call with non-LID JID %s", lid)
	}
	if pn, ok := s.cache.getByLID(lid.User); ok {
		if pn == "" {
			return types.JID{}, nil
		}
		return types.JID{User: pn, Device: lid.Device, Server: types.DefaultUserServer}, nil
	}

	var pn string
	err := s.db.QueryRowContext(ctx, "SELECT pn FROM whatsmeow_lid_map WHERE lid=$1", lid.User).Scan(&pn)
	if errors.Is(err, sql.ErrNoRows) {
		s.cache.put("", lid.User)
		return types.JID{}, nil
	}
	if err != nil {
		return types.JID{}, err
	}
	s.cache.put(pn, lid.User)
	return types.JID{User: pn, Device: lid.Device, Server: types.DefaultUserServer}, nil
}

func (s *boundedLIDStore) GetManyLIDsForPNs(ctx context.Context, pns []types.JID) (map[types.JID]types.JID, error) {
	result := make(map[types.JID]types.JID, len(pns))
	for _, pn := range pns {
		if pn.Server != types.DefaultUserServer {
			continue
		}
		lid, err := s.GetLIDForPN(ctx, pn)
		if err != nil {
			return nil, err
		}
		if lid.User != "" {
			result[pn] = lid.ToNonAD()
		}
	}
	return result, nil
}

func (s *boundedLIDStore) PutLIDMapping(ctx context.Context, lid, pn types.JID) error {
	return s.PutManyLIDMappings(ctx, []store.LIDMapping{{LID: lid, PN: pn}})
}

func (s *boundedLIDStore) PutManyLIDMappings(ctx context.Context, mappings []store.LIDMapping) error {
	valid := make([]store.LIDMapping, 0, len(mappings))
	for _, mapping := range mappings {
		if mapping.LID.Server == types.HiddenUserServer && mapping.PN.Server == types.DefaultUserServer {
			valid = append(valid, mapping)
		}
	}
	if len(valid) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, mapping := range valid {
		if _, err := tx.ExecContext(ctx, "DELETE FROM whatsmeow_lid_map WHERE (lid<>$1 AND pn=$2)", mapping.LID.User, mapping.PN.User); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO whatsmeow_lid_map (lid, pn) VALUES ($1, $2)
			ON CONFLICT (lid) DO UPDATE SET pn=excluded.pn
			WHERE whatsmeow_lid_map.pn<>excluded.pn`, mapping.LID.User, mapping.PN.User); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	for _, mapping := range valid {
		s.cache.put(mapping.PN.User, mapping.LID.User)
	}
	return nil
}
