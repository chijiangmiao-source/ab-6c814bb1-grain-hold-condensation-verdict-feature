package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"grain-ventilation/internal/decision"
)

func mustCreate(t *testing.T, ctx context.Context, st *Store, voyage, hatch string, tg float64) *Assessment {
	t.Helper()
	in := decision.Input{Voyage: voyage, Hatch: hatch, Tg: tg, Ta: 20, RH: 70}
	r, err := decision.Evaluate(in)
	require.NoError(t, err)
	a, err := st.Create(ctx, in, *r)
	require.NoError(t, err)
	return a
}

func TestStore_CreateGetList(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, ":memory:")
	require.NoError(t, err)
	defer st.Close()

	in := decision.Input{Voyage: "VOY-7", Hatch: "2P", Tg: 25, Ta: 20, RH: 70}
	r, err := decision.Evaluate(in)
	require.NoError(t, err)

	created, err := st.Create(ctx, in, *r)
	require.NoError(t, err)
	assert.Equal(t, int64(1), created.ID)
	assert.False(t, created.HasPrev, "first measurement of a hatch has no predecessor")

	got, err := st.Get(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, in, got.Input)
	// Unrounded intermediates survive a round-trip.
	assert.Equal(t, r.Gamma, got.Result.Gamma)
	assert.Equal(t, r.Td, got.Result.Td)
	assert.Equal(t, r.Delta, got.Result.Delta)
	assert.Equal(t, r.Verdict, got.Result.Verdict)
	// Display values are re-derived and rounded.
	assert.Equal(t, 14.36, got.Result.TdDisplay)
	assert.False(t, got.HasPrev)

	list, err := st.List(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, created.ID, list[0].ID)
}

func TestStore_PredecessorChainPerVoyageAndHatch(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, ":memory:")
	require.NoError(t, err)
	defer st.Close()

	// Interleaved submissions across two voyages and three hatches prove the
	// predecessor lookup is scoped by BOTH voyage and hatch and follows
	// creation order (id), never global recency.
	a1 := mustCreate(t, ctx, st, "V-1", "3H", 25)
	b1 := mustCreate(t, ctx, st, "V-1", "4H", 25)
	c1 := mustCreate(t, ctx, st, "V-2", "3H", 25) // same hatch, other voyage
	a2 := mustCreate(t, ctx, st, "V-1", "3H", 24) // must chain to a1
	b2 := mustCreate(t, ctx, st, "V-1", "4H", 23) // must chain to b1
	c2 := mustCreate(t, ctx, st, "V-2", "3H", 22) // must chain to c1
	a3 := mustCreate(t, ctx, st, "V-1", "3H", 26) // must chain to a2

	require.False(t, a1.HasPrev)
	require.False(t, b1.HasPrev)
	require.False(t, c1.HasPrev)
	assert.Equal(t, a1.ID, a2.PrevID)
	assert.Equal(t, b1.ID, b2.PrevID)
	assert.Equal(t, c1.ID, c2.PrevID, "same hatch number on another voyage is a different chain")
	assert.Equal(t, a2.ID, a3.PrevID)

	// The links persist through a read.
	got, err := st.Get(ctx, a3.ID)
	require.NoError(t, err)
	assert.True(t, got.HasPrev)
	assert.Equal(t, a2.ID, got.PrevID)
}

// TestStore_ConcurrentCreatesFormOneChain: even simultaneous valid
// submissions for the same voyage+hatch must form a single linear chain
// (pred 0 -> 1 -> 2 -> ...), never two rows sharing one predecessor.
func TestStore_ConcurrentCreatesFormOneChain(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, ":memory:")
	require.NoError(t, err)
	defer st.Close()

	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			in := decision.Input{Voyage: "V-C", Hatch: "7H", Tg: 25 + float64(i)*0.01, Ta: 20, RH: 70}
			r, err := decision.Evaluate(in)
			if err != nil {
				errCh <- err
				return
			}
			a, err := st.Create(ctx, in, *r)
			if err != nil {
				errCh <- err
				return
			}
			if a.HasPrev && (a.PrevID <= 0 || a.PrevID >= a.ID) {
				errCh <- fmt.Errorf("row %d has bad prev %d", a.ID, a.PrevID)
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for e := range errCh {
		require.NoError(t, e)
	}

	// Walk the chain: exactly one first row, every other id appears exactly
	// once as a predecessor (linear, no forks, no duplicates).
	list, err := st.List(ctx)
	require.NoError(t, err)
	require.Len(t, list, n)
	firstCount := 0
	prevs := map[int64]int{}
	for _, a := range list {
		if a.HasPrev {
			prevs[a.PrevID]++
		} else {
			firstCount++
		}
	}
	assert.Equal(t, 1, firstCount, "exactly one first measurement")
	assert.Len(t, prevs, n-1, "every non-head row points at a unique predecessor")
	for id, count := range prevs {
		assert.LessOrEqual(t, id, int64(n))
		assert.Equal(t, 1, count, "predecessor %d used %d times (chain forked)", id, count)
	}
}

func TestStore_GetMissing(t *testing.T) {
	st, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	defer st.Close()

	_, err = st.Get(context.Background(), 999)
	assert.ErrorIs(t, err, ErrNoRows)
}

// validatedBatch builds already-evaluated measurements the way the HTTP
// handler passes them to CreateMany.
func validatedBatch(t *testing.T, rows ...struct {
	voyage, hatch string
	tg            float64
}) []ValidatedMeasurement {
	t.Helper()
	out := make([]ValidatedMeasurement, 0, len(rows))
	for _, row := range rows {
		in := decision.Input{Voyage: row.voyage, Hatch: row.hatch, Tg: row.tg, Ta: 20, RH: 70}
		r, err := decision.Evaluate(in)
		require.NoError(t, err)
		out = append(out, ValidatedMeasurement{Input: in, Result: *r})
	}
	return out
}

func row(voyage, hatch string, tg float64) struct {
	voyage, hatch string
	tg            float64
} {
	return struct {
		voyage, hatch string
		tg            float64
	}{voyage, hatch, tg}
}

// TestStore_CreateMany_ChainsWithinBatchAndFromDatabase proves the batch
// predecessor rule against an INTERLEAVED and REPEATED-hatch batch: a repeat
// of a voyage+hatch later in the batch links to the earlier in-batch row,
// while a hatch's first in-batch row inherits the latest committed database
// row; a different voyage with the same hatch number is an isolated chain.
func TestStore_CreateMany_ChainsWithinBatchAndFromDatabase(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, ":memory:")
	require.NoError(t, err)
	defer st.Close()

	// Pre-existing chains in the database.
	db1H := mustCreate(t, ctx, st, "V-B", "1H", 25) // id 1
	db2H := mustCreate(t, ctx, st, "V-B", "2H", 25) // id 2
	dbOther := mustCreate(t, ctx, st, "V-OTHER", "1H", 25)

	ms := validatedBatch(t,
		row("V-B", "1H", 24),     // b0 -> db1H (first in-batch appearance)
		row("V-B", "3H", 25),     // b1 -> none (first measurement of 3H)
		row("V-B", "2H", 23),     // b2 -> db2H
		row("V-B", "1H", 26),     // b3 -> b0 (repeat later in the SAME batch)
		row("V-B", "3H", 22),     // b4 -> b1 (in-batch repeat, interleaved order)
		row("V-OTHER", "1H", 24), // b5 -> dbOther, not any V-B row
	)

	created, err := st.CreateMany(ctx, ms)
	require.NoError(t, err)
	require.Len(t, created, 6)

	// Ids follow the measurement order and are contiguous.
	for i, a := range created {
		assert.Equal(t, int64(4+i), a.ID, "row %d keeps its array position in id order", i)
	}

	wantPrev := []struct {
		has  bool
		prev int64
	}{
		{true, db1H.ID},
		{false, 0},
		{true, db2H.ID},
		{true, created[0].ID},
		{true, created[1].ID},
		{true, dbOther.ID},
	}
	for i, want := range wantPrev {
		assert.Equal(t, want.has, created[i].HasPrev, "row %d HasPrev", i)
		assert.Equal(t, want.prev, created[i].PrevID, "row %d PrevID", i)
	}

	// Links persist through a reload.
	got, err := st.Get(ctx, created[3].ID)
	require.NoError(t, err)
	assert.True(t, got.HasPrev)
	assert.Equal(t, created[0].ID, got.PrevID)

	// The overview immediately reflects the true creation order: each hatch's
	// MAX(id) is its LAST batch row.
	latest, err := st.LatestByVoyage(ctx, "V-B")
	require.NoError(t, err)
	require.Len(t, latest, 3)
	byHatch := map[string]int64{}
	for _, a := range latest {
		byHatch[a.Input.Hatch] = a.ID
	}
	assert.Equal(t, created[3].ID, byHatch["1H"])
	assert.Equal(t, created[2].ID, byHatch["2H"])
	assert.Equal(t, created[4].ID, byHatch["3H"])

	list, err := st.List(ctx)
	require.NoError(t, err)
	assert.Len(t, list, 9)
}

// An empty batch is a harmless no-op.
func TestStore_CreateMany_Empty(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, ":memory:")
	require.NoError(t, err)
	defer st.Close()

	out, err := st.CreateMany(ctx, nil)
	require.NoError(t, err)
	assert.Nil(t, out)

	list, err := st.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, list)
}

// TestStore_CreateMany_RollsBackWhenAnInsertFails proves the atomicity
// guarantee at the storage layer: a failure midway through the batch rolls
// back EVERY earlier row, leaving no partial records. Real traffic cannot
// trigger this (rows are pre-validated), so a trigger forces the SECOND
// insert to fail inside the same transaction.
func TestStore_CreateMany_RollsBackWhenAnInsertFails(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, ":memory:")
	require.NoError(t, err)
	defer st.Close()

	// Abort only from the second row onward: inside the batch transaction the
	// first insert is already visible to the trigger's count.
	_, err = st.db.ExecContext(ctx, `
CREATE TRIGGER fail_second_batch_insert
AFTER INSERT ON assessments
WHEN (SELECT COUNT(*) FROM assessments) >= 2
BEGIN
    SELECT RAISE(ABORT, 'forced mid-batch failure');
END`)
	require.NoError(t, err)

	ms := validatedBatch(t,
		row("V-FAIL", "1H", 25),
		row("V-FAIL", "2H", 24),
		row("V-FAIL", "3H", 23),
	)
	_, err = st.CreateMany(ctx, ms)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "forced mid-batch failure")

	list, err := st.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, list, "no partial batch and no broken predecessor may survive")
}

// mustCreateAt is the timestamp-injecting counterpart of mustCreate: it lets
// a test give two rows the SAME created_at so latest-per-hatch selection is
// forced onto the record id instead of timestamp recency.
func mustCreateAt(t *testing.T, ctx context.Context, st *Store, at time.Time, voyage, hatch string, tg float64) *Assessment {
	t.Helper()
	in := decision.Input{Voyage: voyage, Hatch: hatch, Tg: tg, Ta: 20, RH: 70}
	r, err := decision.Evaluate(in)
	require.NoError(t, err)
	a, err := st.createAt(ctx, in, *r, at)
	require.NoError(t, err)
	return a
}

func TestStore_LatestByVoyage_InterleavedPicksMaxIDPerHatch(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, ":memory:")
	require.NoError(t, err)
	defer st.Close()

	// Two voyages, several hatches, interleaved. One hatch (V-1/3H) is
	// measured repeatedly. The overview must return exactly the MAX(id) row
	// of EACH hatch of the requested voyage and nothing from V-2.
	a1 := mustCreate(t, ctx, st, "V-1", "3H", 25) // V-1 3H #1
	b1 := mustCreate(t, ctx, st, "V-1", "2P", 24) // V-1 2P only
	x1 := mustCreate(t, ctx, st, "V-2", "3H", 5)  // same hatch no., other voyage
	a2 := mustCreate(t, ctx, st, "V-1", "3H", 23) // V-1 3H #2
	x2 := mustCreate(t, ctx, st, "V-2", "3H", 6)
	a3 := mustCreate(t, ctx, st, "V-1", "3H", 26) // V-1 3H latest
	c1 := mustCreate(t, ctx, st, "V-1", "4H", 20)

	// The repeated measurements really are three distinct ids in order.
	require.Less(t, a1.ID, a2.ID)
	require.Less(t, a2.ID, a3.ID)
	require.Less(t, x1.ID, x2.ID)

	got, err := st.LatestByVoyage(ctx, "V-1")
	require.NoError(t, err)
	require.Len(t, got, 3, "one snapshot per hatch of V-1")

	byHatch := map[string]*Assessment{}
	for _, a := range got {
		byHatch[a.Input.Hatch] = a
		assert.Equal(t, "V-1", a.Input.Voyage, "no row may come from another voyage")
	}
	assert.Equal(t, a3.ID, byHatch["3H"].ID, "3H latest is the biggest id, not a1/a2")
	assert.Equal(t, b1.ID, byHatch["2P"].ID)
	assert.Equal(t, c1.ID, byHatch["4H"].ID)
	// The snapshot carries the full persisted evaluation, not a stale copy.
	assert.Equal(t, a3.Result.Verdict, byHatch["3H"].Result.Verdict)
	assert.Equal(t, a3.Result.Delta, byHatch["3H"].Result.Delta)

	// The V-2 hatch is isolated even though it shares the hatch number.
	got2, err := st.LatestByVoyage(ctx, "V-2")
	require.NoError(t, err)
	require.Len(t, got2, 1)
	assert.Equal(t, x2.ID, got2[0].ID)
	assert.Equal(t, "V-2", got2[0].Input.Voyage)
}

// Same created_at for every row (including the repeated measurements) must
// not change which record is chosen: latestness follows MAX(id), not the
// timestamp. A deliberately back-dated newer row proves the same point.
func TestStore_LatestByVoyage_SameTimestampPicksByID(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, ":memory:")
	require.NoError(t, err)
	defer st.Close()

	base := time.Date(2026, 9, 13, 8, 0, 0, 0, time.UTC)

	// Two rows for the same hatch with an IDENTICAL created_at: the later
	// inserted id must win.
	same1 := mustCreateAt(t, ctx, st, base, "V-T", "1H", 25)
	same2 := mustCreateAt(t, ctx, st, base, "V-T", "1H", 23)
	require.NotEqual(t, same1.ID, same2.ID)

	// A third row that is newer by id but EARLIER by its (skewed) timestamp
	// must still win: id is the source of truth for latestness.
	skewed := mustCreateAt(t, ctx, st, base.Add(-time.Hour), "V-T", "1H", 26)

	// Another hatch, same shared timestamp, anchors the ordering check.
	other := mustCreateAt(t, ctx, st, base, "V-T", "2H", 24)

	got, err := st.LatestByVoyage(ctx, "V-T")
	require.NoError(t, err)
	require.Len(t, got, 2, "exactly one row per hatch despite equal timestamps")

	assert.Equal(t, "1H", got[0].Input.Hatch, "sorted by hatch ascending")
	assert.Equal(t, "2H", got[1].Input.Hatch)
	assert.Equal(t, skewed.ID, got[0].ID,
		"latest is MAX(id) even though its created_at is earlier")
	assert.Equal(t, other.ID, got[1].ID)
}

// Hatch ordering is stable and deterministic: hatch ascending, then id.
func TestStore_LatestByVoyage_StableHatchOrdering(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, ":memory:")
	require.NoError(t, err)
	defer st.Close()

	// Insert in scrambled hatch order.
	for _, hatch := range []string{"4H", "1H", "3H", "2H"} {
		mustCreate(t, ctx, st, "V-O", hatch, 25)
	}
	got, err := st.LatestByVoyage(ctx, "V-O")
	require.NoError(t, err)
	require.Len(t, got, 4)
	assert.Equal(t, []string{"1H", "2H", "3H", "4H"},
		[]string{got[0].Input.Hatch, got[1].Input.Hatch, got[2].Input.Hatch, got[3].Input.Hatch})

	// Calling twice returns the same order (no GROUP BY rowid jitter).
	gotAgain, err := st.LatestByVoyage(ctx, "V-O")
	require.NoError(t, err)
	for i := range got {
		assert.Equal(t, got[i].ID, gotAgain[i].ID)
	}
}

// An unknown voyage is an empty non-nil collection, never an error.
func TestStore_LatestByVoyage_UnknownVoyageIsEmpty(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, ":memory:")
	require.NoError(t, err)
	defer st.Close()

	mustCreate(t, ctx, st, "V-KNOWN", "1H", 25)

	got, err := st.LatestByVoyage(ctx, "V-DOES-NOT-EXIST")
	require.NoError(t, err)
	require.NotNil(t, got, "unknown voyage returns an empty slice, not nil")
	assert.Empty(t, got)
}

// TestStore_MigrateFromOldSchema is the storage regression: a database file
// written by the pre-predecessor version (table without prev_id) must open
// losslessly. Historical detail values and list ordering survive, and new
// records can still be created.
func TestStore_MigrateFromOldSchema(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "old.db")

	// 1. Create and populate a database with the ORIGINAL schema, using a
	// bare sql.DB so no new code paths touch it.
	oldDB, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	_, err = oldDB.Exec(`
CREATE TABLE assessments (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    voyage     TEXT    NOT NULL,
    hatch      TEXT    NOT NULL,
    tg         REAL    NOT NULL,
    ta         REAL    NOT NULL,
    rh         REAL    NOT NULL,
    gamma      REAL    NOT NULL,
    td         REAL    NOT NULL,
    delta      REAL    NOT NULL,
    verdict    TEXT    NOT NULL,
    created_at TEXT    NOT NULL
)`)
	require.NoError(t, err)
	oldRows := []struct {
		voyage, hatch, verdict, createdAt string
		tg, ta, rh, gamma, td, delta      float64
	}{
		{"OLD-V", "1P", "allowed", "2026-01-01T00:00:00.000000001Z", 25, 20, 70, 0.98, 14.35, 10.65},
		{"OLD-V", "1P", "denied", "2026-01-02T00:00:00.000000002Z", 5, 28, 95, 1.5, 27.0, -22.0},
		{"OLD-V", "2S", "retest", "2026-01-03T00:00:00.000000003Z", 16.357, 20, 70, 0.98, 14.36, 1.997},
	}
	for _, row := range oldRows {
		_, err = oldDB.Exec(`INSERT INTO assessments
(voyage, hatch, tg, ta, rh, gamma, td, delta, verdict, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			row.voyage, row.hatch, row.tg, row.ta, row.rh,
			row.gamma, row.td, row.delta, row.verdict, row.createdAt)
		require.NoError(t, err)
	}
	require.NoError(t, oldDB.Close())

	// 2. Reopen through the current store: migration must add prev_id
	//    without touching existing data.
	st, err := Open(ctx, dbPath)
	require.NoError(t, err)
	defer st.Close()

	list, err := st.List(ctx)
	require.NoError(t, err)
	require.Len(t, list, 3, "all historical rows survive migration")
	// Newest first, insertion order preserved.
	assert.Equal(t, int64(3), list[0].ID)
	assert.Equal(t, int64(2), list[1].ID)
	assert.Equal(t, int64(1), list[2].ID)
	assert.Equal(t, -22.0, list[1].Result.Delta, "unrounded historical Δ survives")
	assert.Equal(t, "denied", list[1].Result.Verdict)
	for _, a := range list {
		assert.False(t, a.HasPrev, "historical rows start with a NULL predecessor")
	}

	// Historical detail is still readable.
	got, err := st.Get(ctx, 3)
	require.NoError(t, err)
	assert.Equal(t, "OLD-V", got.Input.Voyage)
	assert.Equal(t, "2S", got.Input.Hatch)
	assert.Equal(t, 1.997, got.Result.Delta)
	assert.False(t, got.HasPrev)

	// 3. New records can be created and link against migrated rows: the
	//    predecessor lookup works by (voyage, hatch), so a fresh measurement
	//    for OLD-V/1P chains to that hatch's latest historical row (id 2).
	fresh := mustCreate(t, ctx, st, "OLD-V", "1P", 24)
	assert.Equal(t, int64(4), fresh.ID)
	assert.True(t, fresh.HasPrev)
	assert.Equal(t, int64(2), fresh.PrevID)

	brandNew := mustCreate(t, ctx, st, "NEW-V", "9H", 25)
	assert.False(t, brandNew.HasPrev, "brand new voyage+hatch is still a first measurement")
}

// TestStore_MigrationIsIdempotent: opening an already-current database must
// not error or duplicate anything.
func TestStore_MigrationIsIdempotent(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "now.db")
	st1, err := Open(ctx, dbPath)
	require.NoError(t, err)
	mustCreate(t, ctx, st1, "V", "H", 25)
	require.NoError(t, st1.Close())

	st2, err := Open(ctx, dbPath)
	require.NoError(t, err)
	defer st2.Close()
	list, err := st2.List(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, int64(1), list[0].ID)
}
