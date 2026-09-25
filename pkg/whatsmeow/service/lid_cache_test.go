package whatsmeow_service

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"go.mau.fi/whatsmeow/types"
)

func TestLIDPairCacheEvictsLeastRecentlyUsedEntry(t *testing.T) {
	cache := newLIDPairCache(2)
	cache.put("pn-1", "lid-1")
	cache.put("pn-2", "lid-2")

	if _, ok := cache.getByPN("pn-1"); !ok {
		t.Fatal("expected first mapping in cache")
	}
	cache.put("pn-3", "lid-3")

	if _, ok := cache.getByPN("pn-2"); ok {
		t.Fatal("least-recently-used mapping was not evicted")
	}
	if _, ok := cache.getByLID("lid-2"); ok {
		t.Fatal("evicted reverse mapping remains cached")
	}
	if cache.order.Len() != 2 {
		t.Fatalf("cache size = %d, want 2", cache.order.Len())
	}
}

func TestLIDPairCacheUpdatesBothDirections(t *testing.T) {
	cache := newLIDPairCache(2)
	cache.put("pn-1", "lid-old")
	cache.put("pn-1", "lid-new")

	if _, ok := cache.getByLID("lid-old"); ok {
		t.Fatal("old reverse mapping remains after replacement")
	}
	if got, ok := cache.getByLID("lid-new"); !ok || got != "pn-1" {
		t.Fatalf("new reverse mapping = %q, found %v", got, ok)
	}
}

func TestBoundedLIDStoreCachesDatabaseLookup(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	query := "SELECT lid FROM whatsmeow_lid_map WHERE pn=$1"
	mock.ExpectQuery(regexp.QuoteMeta(query)).
		WithArgs("pn-1").
		WillReturnRows(sqlmock.NewRows([]string{"lid"}).AddRow("lid-1"))

	store := newBoundedLIDStore(db)
	pn := types.JID{User: "pn-1", Server: types.DefaultUserServer}
	for i := 0; i < 2; i++ {
		lid, err := store.GetLIDForPN(context.Background(), pn)
		if err != nil {
			t.Fatal(err)
		}
		if lid.User != "lid-1" || lid.Server != types.HiddenUserServer {
			t.Fatalf("unexpected LID mapping: %#v", lid)
		}
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
