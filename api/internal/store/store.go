// Package store persists assessments in SQLite. The unrounded intermediate
// values (gamma, td, delta) are stored as REAL so a later audit or reload
// reproduces the verdict without re-running the formula on rounded numbers.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"grain-ventilation/internal/decision"

	_ "modernc.org/sqlite"
)

// Assessment is one persisted row plus its computed result.
type Assessment struct {
	ID        int64           `json:"id"`
	Input     decision.Input  `json:"input"`
	Result    decision.Result `json:"result"`
	CreatedAt time.Time       `json:"created_at"`

	// PrevID is the id of the previous valid assessment for the SAME voyage
	// and hatch (HasPrev is false for a hatch's first measurement). It is
	// fixed at creation time and never re-pointed later, even if that row
	// disappears.
	PrevID  int64
	HasPrev bool
}

// Store wraps the SQLite database.
type Store struct {
	db *sql.DB
}

// ErrNoRows is returned by Get for unknown ids.
var ErrNoRows = sql.ErrNoRows

// Open opens (creating if needed) the database at path and applies the
// schema. Use ":memory:" for tests.
func Open(ctx context.Context, path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// SQLite + modernc: a single writer is plenty for this app and avoids
	// "database is locked" under the verify suite's parallel requests. It
	// also serialises the predecessor lookup + insert inside Create so two
	// concurrent submissions can never pick the same predecessor.
	db.SetMaxOpenConns(1)

	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS assessments (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    voyage     TEXT    NOT NULL,
    hatch      TEXT    NOT NULL,
    tg         REAL    NOT NULL,
    ta         REAL    NOT NULL,
    rh         REAL    NOT NULL,
    gamma      REAL    NOT NULL,  -- unrounded Magnus intermediate
    td         REAL    NOT NULL,  -- unrounded dew point
    delta      REAL    NOT NULL,  -- unrounded Tg - Td, basis of verdict
    verdict    TEXT    NOT NULL,
    created_at TEXT    NOT NULL,
    -- Predecessor for the same voyage+hatch; NULL on first measurement.
    -- Deliberately NOT a foreign key: a dangling link must be tolerated at
    -- read time (comparison marked unavailable), never block or cascade.
    prev_id    INTEGER
);
CREATE INDEX IF NOT EXISTS idx_assessments_created_at ON assessments(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_assessments_chain ON assessments(voyage, hatch, id DESC);
`); err != nil {
		return err
	}

	// Additive migration for databases created before predecessor linking
	// existed. ALTER TABLE ADD COLUMN is lossless: every historical row
	// simply starts with prev_id = NULL (a first measurement for its chain).
	hasCol, err := s.columnExists(ctx, "assessments", "prev_id")
	if err != nil {
		return err
	}
	if !hasCol {
		if _, err := s.db.ExecContext(ctx,
			`ALTER TABLE assessments ADD COLUMN prev_id INTEGER`); err != nil {
			return fmt.Errorf("migrate assessments.prev_id: %w", err)
		}
		if _, err := s.db.ExecContext(ctx,
			`CREATE INDEX IF NOT EXISTS idx_assessments_chain ON assessments(voyage, hatch, id DESC)`); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) columnExists(ctx context.Context, table, column string) (bool, error) {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// ValidatedMeasurement is one row of a batch submission. The HTTP layer has
// already parsed the row and decision.Evaluate has confirmed it is legal and
// produced the result, so the store must never re-validate: it only persists
// the rows in order and fixes their predecessor links.
type ValidatedMeasurement struct {
	Input  decision.Input
	Result decision.Result
}

// Create inserts one assessment and returns it with its creation timestamp.
// Inside one transaction the most recent earlier row for the same voyage and
// hatch (in id/creation order) is locked as this row's predecessor; a first
// measurement for that voyage+hatch gets no predecessor. A rejected request
// never reaches here, so 422s can never become a predecessor.
func (s *Store) Create(ctx context.Context, in decision.Input, r decision.Result) (*Assessment, error) {
	return s.createAt(ctx, in, r, time.Now().UTC())
}

// createAt is the clock-injectable core of Create. Production traffic always
// goes through Create; tests call this directly to build rows that share a
// creation timestamp (or carry a skewed one), proving the "latest per hatch"
// query chooses by record id rather than by the timestamp text.
func (s *Store) createAt(ctx context.Context, in decision.Input, r decision.Result, now time.Time) (*Assessment, error) {
	createdAt := now.Format(time.RFC3339Nano)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit

	var prev sql.NullInt64
	err = tx.QueryRowContext(ctx, `
SELECT id FROM assessments
WHERE voyage = ? AND hatch = ?
ORDER BY id DESC
LIMIT 1`, in.Voyage, in.Hatch).Scan(&prev)
	switch {
	case err == sql.ErrNoRows:
		// First measurement for this voyage+hatch: no predecessor.
	case err != nil:
		return nil, fmt.Errorf("lock predecessor: %w", err)
	}

	res, err := tx.ExecContext(ctx, `
INSERT INTO assessments (voyage, hatch, tg, ta, rh, gamma, td, delta, verdict, created_at, prev_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.Voyage, in.Hatch, in.Tg, in.Ta, in.RH,
		r.Gamma, r.Td, r.Delta, r.Verdict, createdAt, prev)
	if err != nil {
		return nil, fmt.Errorf("insert assessment: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit assessment: %w", err)
	}

	a := &Assessment{ID: id, Input: in, Result: r, CreatedAt: now}
	if prev.Valid {
		a.PrevID = prev.Int64
		a.HasPrev = true
	}
	return a, nil
}

// CreateMany persists an ORDERED batch of already-validated measurements in a
// SINGLE transaction. Rows are inserted strictly in slice order, so their
// ids follow the measurement order the chief officer took them.
//
// The predecessor of each row is the most recent EARLIER row of the same
// voyage+hatch: a row inserted earlier in THIS batch wins (rows of the same
// voyage+hatch chain inside the batch, e.g. two 3H measurements separated by
// other hatches), otherwise the lookup falls back to the latest valid row
// already in the database; a brand new voyage+hatch gets no predecessor.
//
// Because the whole batch shares one transaction, any insert failure rolls
// back every earlier row too: the database can never retain a partial batch
// or a predecessor link that points past a missing row. The caller validates
// every row first, so an illegal batch is rejected before CreateMany runs.
func (s *Store) CreateMany(ctx context.Context, ms []ValidatedMeasurement) ([]*Assessment, error) {
	if len(ms) == 0 {
		return nil, nil
	}
	now := time.Now().UTC()
	createdAt := now.Format(time.RFC3339Nano)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit

	// Last in-batch id per voyage+hatch, in insertion order. The struct key
	// (not a concatenated string) cannot alias two pairs even if a code were
	// to contain a NUL byte; slice order guarantees the stored id is the most
	// recent earlier row for that chain.
	type chainKey struct{ voyage, hatch string }
	lastInBatch := map[chainKey]int64{}
	selectPrev, err := tx.PrepareContext(ctx, `
SELECT id FROM assessments
WHERE voyage = ? AND hatch = ?
ORDER BY id DESC
LIMIT 1`)
	if err != nil {
		return nil, fmt.Errorf("prepare predecessor lookup: %w", err)
	}
	defer selectPrev.Close()
	insertRow, err := tx.PrepareContext(ctx, `
INSERT INTO assessments (voyage, hatch, tg, ta, rh, gamma, td, delta, verdict, created_at, prev_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return nil, fmt.Errorf("prepare assessment insert: %w", err)
	}
	defer insertRow.Close()

	out := make([]*Assessment, 0, len(ms))
	for i := range ms {
		in, r := ms[i].Input, ms[i].Result
		key := chainKey{in.Voyage, in.Hatch}

		var prev sql.NullInt64
		if batchPrev, ok := lastInBatch[key]; ok {
			// A repeat of this voyage+hatch later in the SAME batch chains to
			// the earlier in-batch row, never to a database row behind it.
			prev = sql.NullInt64{Int64: batchPrev, Valid: true}
		} else {
			// First appearance in the batch: inherit the chain's latest
			// already-committed predecessor, if any.
			if err := selectPrev.QueryRowContext(ctx, in.Voyage, in.Hatch).Scan(&prev); err != nil {
				if err != sql.ErrNoRows {
					return nil, fmt.Errorf("lock predecessor: %w", err)
				}
				// sql.ErrNoRows: first measurement, prev stays NULL.
			}
		}

		res, err := insertRow.ExecContext(ctx,
			in.Voyage, in.Hatch, in.Tg, in.Ta, in.RH,
			r.Gamma, r.Td, r.Delta, r.Verdict, createdAt, prev)
		if err != nil {
			return nil, fmt.Errorf("insert assessment: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return nil, err
		}
		lastInBatch[key] = id

		a := &Assessment{ID: id, Input: in, Result: r, CreatedAt: now}
		if prev.Valid {
			a.PrevID = prev.Int64
			a.HasPrev = true
		}
		out = append(out, a)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit batch: %w", err)
	}
	return out, nil
}

// List returns all assessments, newest first.
func (s *Store) List(ctx context.Context) ([]*Assessment, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, voyage, hatch, tg, ta, rh, gamma, td, delta, verdict, created_at, prev_id
FROM assessments ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Assessment
	for rows.Next() {
		a, err := scanAssessment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Get returns one assessment by id, or sql.ErrNoRows.
func (s *Store) Get(ctx context.Context, id int64) (*Assessment, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, voyage, hatch, tg, ta, rh, gamma, td, delta, verdict, created_at, prev_id
FROM assessments WHERE id = ?`, id)
	return scanAssessment(row)
}

// LatestByVoyage returns one snapshot per hatch of the given voyage: the
// single most recently created assessment (largest id) for each distinct
// hatch, never two rows for one hatch and never a row from another voyage.
//
// Latestness is decided by the record id (the monotone creation-order key),
// NOT by created_at: submissions landing in the same nanosecond — or rows a
// clock-skew test deliberately back-dates — still resolve to exactly one row
// per hatch because the correlated subquery groups by hatch and takes
// MAX(id). Results are sorted by hatch for a stable presentation, with id as
// a deterministic tiebreaker. An unknown voyage yields an empty (non-nil)
// slice.
func (s *Store) LatestByVoyage(ctx context.Context, voyage string) ([]*Assessment, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT a.id, a.voyage, a.hatch, a.tg, a.ta, a.rh,
       a.gamma, a.td, a.delta, a.verdict, a.created_at, a.prev_id
FROM assessments a
JOIN (
    SELECT hatch, MAX(id) AS max_id
    FROM assessments
    WHERE voyage = ?
    GROUP BY hatch
) latest ON latest.max_id = a.id
ORDER BY a.hatch ASC, a.id ASC`, voyage)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]*Assessment, 0)
	for rows.Next() {
		a, err := scanAssessment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanAssessment(sc rowScanner) (*Assessment, error) {
	var a Assessment
	var createdAt string
	var prev sql.NullInt64
	err := sc.Scan(
		&a.ID, &a.Input.Voyage, &a.Input.Hatch,
		&a.Input.Tg, &a.Input.Ta, &a.Input.RH,
		&a.Result.Gamma, &a.Result.Td, &a.Result.Delta,
		&a.Result.Verdict, &createdAt, &prev)
	if err != nil {
		return nil, err
	}
	a.Result.GammaDisplay = decision.Round2(a.Result.Gamma)
	a.Result.TdDisplay = decision.Round2(a.Result.Td)
	a.Result.DeltaDisplay = decision.Round2(a.Result.Delta)
	if a.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return nil, err
	}
	if prev.Valid {
		a.PrevID = prev.Int64
		a.HasPrev = true
	}
	return &a, nil
}

// SetPrevIDForTest is a narrow test seam that overwrites a row's stored
// predecessor link so HTTP-level tests can exercise the "saved predecessor
// vanished or belongs to another voyage/hatch" boundary, which real traffic
// can never produce. It must not be used outside tests.
func (s *Store) SetPrevIDForTest(ctx context.Context, id, prevID int64) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE assessments SET prev_id = ? WHERE id = ?`, prevID, id); err != nil {
		return fmt.Errorf("set prev_id: %w", err)
	}
	return nil
}

// SetCreatedAtForTest is a narrow test seam that overwrites a row's creation
// timestamp so HTTP-level tests can give two rows the SAME created_at (or a
// skewed one) and prove latest-per-hatch selection uses MAX(id), not the
// timestamp. Real traffic sets created_at once in Create. Tests only.
func (s *Store) SetCreatedAtForTest(ctx context.Context, id int64, at time.Time) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE assessments SET created_at = ? WHERE id = ?`,
		at.Format(time.RFC3339Nano), id); err != nil {
		return fmt.Errorf("set created_at: %w", err)
	}
	return nil
}
