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

// batchRow builds one already-evaluated batch item, the contract the HTTP
// layer hands to CreateBatch (validation happens before the store call).
func batchRow(t *testing.T, voyage, hatch string, tg float64) BatchItem {
	t.Helper()
	in := decision.Input{Voyage: voyage, Hatch: hatch, Tg: tg, Ta: 20, RH: 70}
	r, err := decision.Evaluate(in)
	require.NoError(t, err)
	return BatchItem{Input: in, Result: *r}
}

// TestStore_CreateBatch_InterleavedAndRepeatedHatches proves the batch
// predecessor rules inside one transaction: a repeated hatch later in the
// batch links to the EARLIER BATCH row, while a hatch not yet seen in the
// batch continues the chain already committed in the database.
func TestStore_CreateBatch_InterleavedAndRepeatedHatches(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, ":memory:")
	require.NoError(t, err)
	defer st.Close()

	// Pre-existing committed chains for 1H and 2H.
	p1 := mustCreate(t, ctx, st, "V-B", "1H", 30)
	p2 := mustCreate(t, ctx, st, "V-B", "2H", 29)

	// Five rows in MEASUREMENT order: hatches interleave and 1H repeats.
	items := []BatchItem{
		batchRow(t, "V-B", "1H", 24), // row 1: continues the committed 1H chain -> p1
		batchRow(t, "V-B", "2H", 23), // row 2: continues the committed 2H chain -> p2
		batchRow(t, "V-B", "1H", 26), // row 3: same hatch earlier IN BATCH -> row 1
		batchRow(t, "V-B", "3H", 25), // row 4: first measurement of 3H -> no predecessor
		batchRow(t, "V-B", "1H", 22), // row 5: latest in-batch 1H -> row 3
	}
	saved, err := st.CreateBatch(ctx, items)
	require.NoError(t, err)
	require.Len(t, saved, 5)

	// Submission order is preserved and ids are consecutive (inserted in one
	// transaction, one after another).
	for i, a := range saved {
		assert.Equal(t, items[i].Input.Hatch, a.Input.Hatch)
		if i > 0 {
			assert.Equal(t, saved[i-1].ID+1, a.ID)
		} else {
			assert.Equal(t, p2.ID+1, a.ID)
		}
	}

	assert.True(t, saved[0].HasPrev && saved[0].PrevID == p1.ID, "row 1 takes the DB's latest 1H predecessor")
	assert.True(t, saved[1].HasPrev && saved[1].PrevID == p2.ID, "row 2 takes the DB's latest 2H predecessor")
	assert.True(t, saved[2].HasPrev && saved[2].PrevID == saved[0].ID, "row 3 links the earlier in-batch 1H row, not p1")
	assert.False(t, saved[3].HasPrev, "row 4 is the first 3H measurement anywhere")
	assert.True(t, saved[4].HasPrev && saved[4].PrevID == saved[2].ID, "row 5 links the most recent in-batch 1H row")

	// After the batch, standalone creates continue each chain at the batch's
	// last row (batch rows are ordinary committed records).
	after2H := mustCreate(t, ctx, st, "V-B", "2H", 21)
	assert.Equal(t, saved[1].ID, after2H.PrevID)
	after3H := mustCreate(t, ctx, st, "V-B", "3H", 20)
	assert.Equal(t, saved[3].ID, after3H.PrevID)

	// List reflects true creation order (newest first).
	list, err := st.List(ctx)
	require.NoError(t, err)
	require.Len(t, list, 9)
	assert.Equal(t, after3H.ID, list[0].ID)
	assert.Equal(t, after2H.ID, list[1].ID)
	assert.Equal(t, saved[4].ID, list[2].ID)
	assert.Equal(t, saved[0].ID, list[6].ID)
	assert.Equal(t, p2.ID, list[7].ID)
	assert.Equal(t, p1.ID, list[8].ID)

	// Overview uses MAX(id) per hatch: 1H latest is row 5, 2H the post-batch row.
	latest, err := st.LatestByVoyage(ctx, "V-B")
	require.NoError(t, err)
	require.Len(t, latest, 3)
	byHatch := map[string]*Assessment{}
	for _, a := range latest {
		byHatch[a.Input.Hatch] = a
	}
	assert.Equal(t, saved[4].ID, byHatch["1H"].ID)
	assert.Equal(t, after2H.ID, byHatch["2H"].ID)
	assert.Equal(t, after3H.ID, byHatch["3H"].ID)
}

// A batch whose voyage+hatch has no committed history starts its chains
// inside the batch; the first occurrence is a first measurement.
func TestStore_CreateBatch_StartsNewChainsInBatch(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, ":memory:")
	require.NoError(t, err)
	defer st.Close()

	saved, err := st.CreateBatch(ctx, []BatchItem{
		batchRow(t, "V-NEW", "1H", 25),
		batchRow(t, "V-NEW", "1H", 24),
		batchRow(t, "V-NEW", "1H", 23),
	})
	require.NoError(t, err)
	require.Len(t, saved, 3)
	assert.False(t, saved[0].HasPrev)
	assert.Equal(t, saved[0].ID, saved[1].PrevID)
	assert.Equal(t, saved[1].ID, saved[2].PrevID)
}

// TestStore_CreateBatch_MidBatchFailureRollsEverythingBack simulates a real
// persistence failure on a MIDDLE insert (a trigger aborts one specific row).
// The whole transaction must roll back: none of the earlier rows survive and
// no predecessor link is left dangling.
func TestStore_CreateBatch_MidBatchFailureRollsEverythingBack(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, ":memory:")
	require.NoError(t, err)
	defer st.Close()

	before := mustCreate(t, ctx, st, "V-RB", "1H", 30)

	// Abort the insert of the sentinel hatch in the MIDDLE of the batch.
	_, err = st.db.ExecContext(ctx, `
CREATE TRIGGER trg_fail_batch
BEFORE INSERT ON assessments
WHEN NEW.hatch = 'BOOM'
BEGIN
	SELECT RAISE(ABORT, 'simulated mid-batch storage failure');
END`)
	require.NoError(t, err)

	_, err = st.CreateBatch(ctx, []BatchItem{
		batchRow(t, "V-RB", "1H", 25), // would chain to `before`
		batchRow(t, "V-RB", "BOOM", 24),
		batchRow(t, "V-RB", "1H", 23),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "simulated mid-batch storage failure")

	list, err := st.List(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1, "no batch row may survive the rollback")
	assert.Equal(t, before.ID, list[0].ID)

	// The chain is intact: a later valid create still links the pre-batch row.
	after := mustCreate(t, ctx, st, "V-RB", "1H", 22)
	assert.Equal(t, before.ID, after.PrevID, "no broken predecessor link after rollback")
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
