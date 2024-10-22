package tmsqlite

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/trace"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmstore"
)

// Store is a single type satisfying all the [tmstore] interfaces.
type Store struct {
	// The string "purego" or "cgo" depending on build tags.
	BuildType string

	// Due to transaction locking behaviors of sqlite
	// (see: https://www.sqlite.org/lang_transaction.html),
	// and the way they interact with the Go SQL drivers,
	// it is better to maintain two separate connection pools.
	ro, rw *sql.DB

	hs  tmconsensus.HashScheme
	reg *gcrypto.Registry
}

func NewOnDiskStore(
	ctx context.Context,
	dbPath string,
	hashScheme tmconsensus.HashScheme,
	reg *gcrypto.Registry,
) (*Store, error) {
	dbPath = filepath.Clean(dbPath)
	if _, err := os.Stat(dbPath); err != nil {
		// Create a file for the database;
		// if no file exists, then our startup pragma commands fail.
		if os.IsNotExist(err) {
			// The file did not exist so we need to create it.
			// We don't use os.Create since that will truncate an existing file.
			// While very unlikely that we would be racing to create a file,
			// it is much better to be defensive about it.
			f, err := os.OpenFile(dbPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return nil, fmt.Errorf("failed to create empty database file: %w", err)
			}
			if err := f.Close(); err != nil {
				return nil, fmt.Errorf("failed to close new empty database file: %w", err)
			}
		} else {
			return nil, fmt.Errorf("failed to stat path %q: %w", dbPath, err)
		}
	}

	// In contrast to the in-memory store,
	// we only have to mark this connection mode as read-write.
	// In combination with the SetMaxOpenConns(1) call,
	// this allows only a single writer at a time;
	// instead of other writers getting an ephemeral "table is locked"
	// or "database is locked" error, they will simply block
	// while contending for the single available connection.
	uri := "file:" + dbPath + "?mode=rw"

	// The driver type comes from the sqlitedriver_*.go file
	// chosen based on build tags.
	rw, err := sql.Open(sqliteDriverType, uri)
	if err != nil {
		return nil, fmt.Errorf("error opening read-write database: %w", err)
	}

	rw.SetMaxOpenConns(1)

	// Unlike other pragmas, this is persistent,
	// and it is only relevant to on-disk databases.
	if _, err := rw.ExecContext(ctx, `PRAGMA journal_mode = WAL`); err != nil {
		return nil, fmt.Errorf("failed to set journal_mode=WAL: %w", err)
	}

	if err := pragmasRW(ctx, rw); err != nil {
		return nil, err
	}

	if err := migrate(ctx, rw); err != nil {
		return nil, err
	}

	// Change mode=rw to mode=ro (since we know that was the final query parameter).
	uri = uri[:len(uri)-1] + "o"
	ro, err := sql.Open(sqliteDriverType, uri)
	if err != nil {
		return nil, fmt.Errorf("error opening read-only database: %w", err)
	}
	if err := pragmasRO(ctx, ro); err != nil {
		return nil, err
	}

	return &Store{
		BuildType: sqliteBuildType,

		rw: rw,
		ro: ro,

		hs:  hashScheme,
		reg: reg,
	}, nil
}

var inMemNameCounter uint32

func NewInMemStore(
	ctx context.Context,
	hashScheme tmconsensus.HashScheme,
	reg *gcrypto.Registry,
) (*Store, error) {
	dbName := fmt.Sprintf("db%0000d", atomic.AddUint32(&inMemNameCounter, 1))
	uri := "file:" + dbName +
		// Give the "file" a unique name so that multiple connections within one process
		// can use the same in-memory database.
		// Standard query parameter: https://www.sqlite.org/uri.html#recognized_query_parameters
		"?mode=memory" +
		// The cache can only be shared or private.
		// A private cache means every connection would see a unique database,
		// so this must be shared.
		"&cache=shared" +
		// Both SQLite wrappers support _txlock.
		// Immediate effectively takes a write lock on the database
		// at the beginning of every transaction.
		// https://www.sqlite.org/lang_transaction.html#deferred_immediate_and_exclusive_transactions
		"&_txlock=immediate"

	// The driver type comes from the sqlitedriver_*.go file
	// chosen based on build tags.
	rw, err := sql.Open(sqliteDriverType, uri)
	if err != nil {
		return nil, fmt.Errorf("error opening read-write database: %w", err)
	}

	// Without limiting it to one open connection,
	// we would get frequent "table is locked" errors.
	// These errors, as far as I can tell,
	// do not automatically resolve with the busy timeout handler.
	// So, only allow one active write connection to the database at a time.
	rw.SetMaxOpenConns(1)

	// We don't set journal mode to WAL with the in-memory store,
	// like we do at this point in the on-disk store.

	if err := pragmasRW(ctx, rw); err != nil {
		return nil, err
	}

	if err := migrate(ctx, rw); err != nil {
		return nil, err
	}

	// It would be nice if there was a way to mark this connection as read-only,
	// but that does not appear possible with the drivers available
	// (you have to connect to an on-disk database for that).
	// We use an identical connection URI except for removing the txlock directive.
	var ok bool
	uri, ok = strings.CutSuffix(uri, "&_txlock=immediate")
	if !ok {
		panic(fmt.Errorf("BUG: failed to cut _txlock suffix from uri %q", uri))
	}
	ro, err := sql.Open(sqliteDriverType, uri)
	if err != nil {
		return nil, fmt.Errorf("error opening read-only database: %w", err)
	}
	if err := pragmasRO(ctx, ro); err != nil {
		return nil, err
	}

	return &Store{
		BuildType: sqliteBuildType,

		rw: rw,
		ro: ro,

		hs:  hashScheme,
		reg: reg,
	}, nil
}

func (s *Store) Close() error {
	errRO := s.ro.Close()
	if errRO != nil {
		errRO = fmt.Errorf("error closing read-only database: %w", errRO)
	}
	errRW := s.rw.Close()
	if errRW != nil {
		errRW = fmt.Errorf("error closing read-write database: %w", errRW)
	}

	return errors.Join(errRO, errRW)
}

func (s *Store) SetNetworkHeightRound(
	ctx context.Context,
	votingHeight uint64, votingRound uint32,
	committingHeight uint64, committingRound uint32,
) error {
	defer trace.StartRegion(ctx, "SetNetworkHeightRound").End()

	_, err := s.rw.ExecContext(
		ctx,
		`UPDATE mirror SET vh = ?, vr = ?, ch = ?, cr = ? WHERE id=0`,
		votingHeight, votingRound, committingHeight, committingRound,
	)
	return err
}

func (s *Store) NetworkHeightRound(ctx context.Context) (
	votingHeight uint64, votingRound uint32,
	committingHeight uint64, committingRound uint32,
	err error,
) {
	defer trace.StartRegion(ctx, "NetworkHeightRound").End()

	err = s.ro.QueryRowContext(
		ctx,
		`SELECT vh, vr, ch, cr FROM mirror WHERE id=0`,
	).Scan(
		&votingHeight, &votingRound,
		&committingHeight, &committingRound,
	)
	if err == nil &&
		votingHeight == 0 && votingRound == 0 &&
		committingHeight == 0 && committingRound == 0 {
		return 0, 0, 0, 0, tmstore.ErrStoreUninitialized
	}
	return
}

func (s *Store) SetStateMachineHeightRound(
	ctx context.Context,
	height uint64, round uint32,
) error {
	defer trace.StartRegion(ctx, "SetStateMachineHeightRound").End()

	_, err := s.rw.ExecContext(
		ctx,
		`UPDATE state_machine SET h = ?, r = ? WHERE id=0`,
		height, round,
	)
	return err
}

func (s *Store) StateMachineHeightRound(ctx context.Context) (
	height uint64, round uint32,
	err error,
) {
	defer trace.StartRegion(ctx, "StateMachineHeightRound").End()

	err = s.ro.QueryRowContext(
		ctx,
		`SELECT h, r FROM state_machine WHERE id=0`,
	).Scan(
		&height, &round,
	)
	if err == nil &&
		height == 0 && round == 0 {
		return 0, 0, tmstore.ErrStoreUninitialized
	}
	return
}

func (s *Store) SavePubKeys(ctx context.Context, keys []gcrypto.PubKey) (string, error) {
	defer trace.StartRegion(ctx, "SavePubKeys").End()

	hash, err := s.hs.PubKeys(keys)
	if err != nil {
		return "", fmt.Errorf("failed to calculate public key hash: %w", err)
	}

	// First check if the hash is already in the database.
	// (Assuming we aren't racing in core Gordian to add this;
	// if we are racing, then this just needs to move inside the transaction.)
	var count int
	err = s.ro.QueryRowContext(
		ctx,
		// Would this be better as an EXISTS query?
		`SELECT COUNT(hash) FROM validator_pub_key_hashes WHERE hash = ?;`,
		hash,
	).Scan(&count)
	if err != nil {
		return "", fmt.Errorf("failed to check public key hash existence: %w", err)
	}

	if count > 0 {
		return string(hash), tmstore.PubKeysAlreadyExistError{
			ExistingHash: string(hash),
		}
	}

	// Otherwise the count was zero, so we need to do all the work.
	tx, err := s.rw.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("failed to open transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := s.createPubKeysInTx(ctx, tx, hash, keys); err != nil {
		return "", err
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("failed to commit saving public key hash: %w", err)
	}

	return string(hash), nil
}

// createPubKeysInTx saves the set of public keys belonging to the given hash.
// This method assumes that the hash does not exist in the validator_pub_key_hashes table yet.
func (s *Store) createPubKeysInTx(
	ctx context.Context,
	tx *sql.Tx,
	pubKeyHash []byte,
	keys []gcrypto.PubKey,
) (hashID int64, err error) {
	defer trace.StartRegion(ctx, "createPubKeysInTx").End()

	// Create the key hash first.
	res, err := tx.ExecContext(
		ctx,
		`INSERT INTO validator_pub_key_hashes(hash, n_keys) VALUES(?,?);`,
		pubKeyHash, len(keys),
	)
	if err != nil {
		return -1, fmt.Errorf("failed to save new public key hash: %w", err)
	}

	hashID, err = res.LastInsertId()
	if err != nil {
		return -1, fmt.Errorf("failed to get last insert ID after saving new public key hash: %w", err)
	}

	args := make([]any, 0, 3*len(keys))
	for i, k := range keys {
		res, err := tx.ExecContext(
			ctx,
			`INSERT OR IGNORE INTO validator_pub_keys(type, key) VALUES(?,?);`,
			k.TypeName(), k.PubKeyBytes(),
		)
		if err != nil {
			return -1, fmt.Errorf("failed to insert validator public key: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return -1, fmt.Errorf("failed to get rows affected after inserting validator public key: %w", err)
		}

		var keyID int64

		if n == 1 {
			keyID, err = res.LastInsertId()
			if err != nil {
				return -1, fmt.Errorf("failed to get insert ID after inserting validator public key: %w", err)
			}
		} else {
			// No rows affected, so we need to query the ID.
			if err := tx.QueryRowContext(
				ctx,
				`SELECT id FROM validator_pub_keys WHERE type = ? AND key = ?;`,
				k.TypeName(), k.PubKeyBytes(),
			).Scan(&keyID); err != nil {
				return -1, fmt.Errorf("failed to get key ID when querying: %w", err)
			}
		}

		// Now that we have a hash ID, key ID, and ordinal index,
		// we can update args we will insert into the pub key hash entries.
		args = append(args, hashID, i, keyID)
	}

	q := `INSERT INTO validator_pub_key_hash_entries(hash_id, idx, key_id) VALUES (?,?,?)` +
		strings.Repeat(", (?,?,?)", len(keys)-1)
	if _, err := tx.ExecContext(ctx, q, args...); err != nil {
		return -1, fmt.Errorf("failed to insert public key hash entry: %w", err)
	}

	return hashID, nil
}

func (s *Store) LoadPubKeys(ctx context.Context, hash string) ([]gcrypto.PubKey, error) {
	defer trace.StartRegion(ctx, "LoadPubKeys").End()

	rows, err := s.ro.QueryContext(
		ctx,
		`SELECT type, key FROM validator_pub_keys_for_hash WHERE hash = ? ORDER BY idx ASC`,
		// This is annoying: if you leave the hash as a string,
		// the string type apparently won't match the blob type,
		// and so you get a misleading empty result.
		[]byte(hash),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query public keys by hash: %w", err)
	}
	defer rows.Close()

	var keys []gcrypto.PubKey
	var keyBytes []byte
	var typeName string
	for rows.Next() {
		keyBytes = keyBytes[:0]
		if err := rows.Scan(&typeName, &keyBytes); err != nil {
			return nil, fmt.Errorf("failed to scan validator hash row: %w", err)
		}

		key, err := s.reg.Decode(typeName, bytes.Clone(keyBytes))
		if err != nil {
			return nil, fmt.Errorf("failed to decode validator key %x: %w", keyBytes, err)
		}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return nil, tmstore.NoPubKeyHashError{Want: hash}
	}

	return keys, nil
}

func (s *Store) SaveVotePowers(ctx context.Context, powers []uint64) (string, error) {
	defer trace.StartRegion(ctx, "SaveVotePowers").End()

	hash, err := s.hs.VotePowers(powers)
	if err != nil {
		return "", fmt.Errorf("failed to calculate vote power hash: %w", err)
	}

	// First check if the hash is already in the database.
	// (Assuming we aren't racing in core Gordian to add this;
	// if we are racing, then this just needs to move inside the transaction.)
	var count int
	err = s.ro.QueryRowContext(
		ctx,
		`SELECT COUNT(hash) FROM validator_power_hashes WHERE hash = ?;`,
		hash,
	).Scan(&count)
	if err != nil {
		return "", fmt.Errorf("failed to check vote power hash existence: %w", err)
	}

	if count > 0 {
		return string(hash), tmstore.VotePowersAlreadyExistError{
			ExistingHash: string(hash),
		}
	}

	// Otherwise the count was zero, so we need to do all the work.
	tx, err := s.rw.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("failed to open transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := s.createVotePowersInTx(ctx, tx, hash, powers); err != nil {
		return "", err
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("failed to commit saving vote power hash: %w", err)
	}

	return string(hash), nil
}

// createVotePowersInTx saves the set of vote powers belonging to the given hash.
// This method assumes that the hash does not exist in the validator_vote_power_hashes table yet.
func (s *Store) createVotePowersInTx(
	ctx context.Context,
	tx *sql.Tx,
	votePowerHash []byte,
	powers []uint64,
) (hashID int64, err error) {
	defer trace.StartRegion(ctx, "createVotePowersInTx").End()

	// Create the key hash first.
	res, err := tx.ExecContext(
		ctx,
		`INSERT INTO validator_power_hashes(hash, n_powers) VALUES(?,?);`,
		votePowerHash, len(powers),
	)
	if err != nil {
		return -1, fmt.Errorf("failed to save new vote power hash: %w", err)
	}

	hashID, err = res.LastInsertId()
	if err != nil {
		return -1, fmt.Errorf("failed to get last insert ID after saving new vote power hash: %w", err)
	}

	args := make([]any, 0, 3*len(powers))
	// TODO: condense this into one larger insert.
	for i, power := range powers {
		args = append(args, hashID, i, power)
	}

	q := `INSERT INTO validator_power_hash_entries(hash_id, idx, power)
VALUES (?,?,?)` +
		strings.Repeat(", (?,?,?)", len(powers)-1)
	if _, err := tx.ExecContext(ctx, q, args...); err != nil {
		return -1, fmt.Errorf("failed to insert vote power hash entry: %w", err)
	}

	return hashID, nil
}

func (s *Store) LoadVotePowers(ctx context.Context, hash string) ([]uint64, error) {
	defer trace.StartRegion(ctx, "LoadVotePowers").End()

	rows, err := s.ro.QueryContext(
		ctx,
		`
SELECT validator_power_hash_entries.power FROM validator_power_hash_entries
  JOIN validator_power_hashes ON validator_power_hashes.id = validator_power_hash_entries.hash_id
  WHERE validator_power_hashes.hash = ? ORDER BY validator_power_hash_entries.idx ASC;`,
		// This is annoying: if you leave the hash as a string,
		// the string type apparently won't match the blob type,
		// and so you get a misleading empty result.
		[]byte(hash),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query vote powers by hash: %w", err)
	}
	defer rows.Close()

	var powers []uint64
	for rows.Next() {
		var pow uint64
		if err := rows.Scan(&pow); err != nil {
			return nil, fmt.Errorf("failed to scan validator power row: %w", err)
		}
		powers = append(powers, pow)
	}

	if len(powers) == 0 {
		return nil, tmstore.NoVotePowerHashError{Want: hash}
	}

	return powers, nil
}

func (s *Store) LoadValidators(ctx context.Context, keyHash, powHash string) ([]tmconsensus.Validator, error) {
	defer trace.StartRegion(ctx, "LoadValidators").End()

	rows, err := s.ro.QueryContext(
		ctx,
		`SELECT keys.type, keys.key, powers.power FROM
(
  SELECT type, key, idx FROM validator_pub_keys_for_hash WHERE hash = ? ORDER BY idx ASC
) as keys
JOIN
(
  SELECT power, idx FROM validator_powers_for_hash WHERE hash = ? ORDER BY idx ASC
) as powers ON keys.idx = powers.idx
`,
		[]byte(keyHash), []byte(powHash),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query for validators: %w", err)
	}
	defer rows.Close()

	var vals []tmconsensus.Validator
	var typeName string
	var keyBytes []byte
	for rows.Next() {
		keyBytes = keyBytes[:0]
		var pow uint64
		if err := rows.Scan(&typeName, &keyBytes, &pow); err != nil {
			return nil, fmt.Errorf("failed to scan validator row: %w", err)
		}

		key, err := s.reg.Decode(typeName, bytes.Clone(keyBytes))
		if err != nil {
			return nil, fmt.Errorf("failed to decode validator key %x: %w", keyBytes, err)
		}

		vals = append(vals, tmconsensus.Validator{
			PubKey: key,
			Power:  pow,
		})
	}
	rows.Close()

	if len(vals) > 0 {
		// One more check that the key count is correct.
		// There is probably some way to push this into the prior query.
		row := s.ro.QueryRowContext(
			ctx,
			`SELECT keys.n_keys, powers.n_powers FROM
(SELECT n_keys FROM validator_pub_key_hashes WHERE hash = ?) AS keys,
(SELECT n_powers FROM validator_power_hashes WHERE hash = ?) AS powers`,
			[]byte(keyHash), []byte(powHash),
		)
		var nKeys, nPows int
		if err := row.Scan(&nKeys, &nPows); err != nil {
			return nil, fmt.Errorf("failed to scan count results: %w", err)
		}
		if nKeys != nPows {
			return nil, tmstore.PubKeyPowerCountMismatchError{
				NPubKeys:   nKeys,
				NVotePower: nPows,
			}
		}
		if nKeys != len(vals) {
			panic(fmt.Errorf(
				"BUG: expected %d vals, but queried %d", nKeys, len(vals),
			))
		}

		return vals, nil
	}

	// We are missing at least one hash.
	// Run another query to determine which we are missing.
	row := s.ro.QueryRowContext(
		ctx,
		`SELECT * FROM
(SELECT COUNT(hash) FROM validator_pub_key_hashes WHERE hash = ?),
(SELECT COUNT(hash) FROM validator_power_hashes WHERE hash = ?)`,
		[]byte(keyHash), []byte(powHash),
	)

	var nKeys, nPows int
	if err := row.Scan(&nKeys, &nPows); err != nil {
		return nil, fmt.Errorf("failed to scan hash counts: %w", err)
	}

	var hashErr error
	if nKeys == 0 {
		hashErr = tmstore.NoPubKeyHashError{Want: keyHash}
	}
	if nPows == 0 {
		hashErr = errors.Join(hashErr, tmstore.NoVotePowerHashError{Want: powHash})
	}

	if hashErr == nil {
		panic(fmt.Errorf(
			"DATA RACE: hashes (pub keys: %x; power: %x) were missing and then appeared",
			keyHash, powHash,
		))
	}
	return nil, hashErr
}

func (s *Store) SaveFinalization(
	ctx context.Context,
	height uint64, round uint32,
	blockHash string,
	valSet tmconsensus.ValidatorSet,
	appStateHash string,
) error {
	defer trace.StartRegion(ctx, "SaveFinalization").End()

	// We're going to need to touch a couple tables, so do this all within a transaction.
	tx, err := s.rw.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to open transaction: %w", err)
	}
	defer tx.Rollback()

	// Check if the validator hashes already exist.
	row := tx.QueryRowContext(
		ctx,
		`SELECT * FROM
(SELECT COUNT(hash) FROM validator_pub_key_hashes WHERE hash = ?),
(SELECT COUNT(hash) FROM validator_power_hashes WHERE hash = ?)`,
		valSet.PubKeyHash, valSet.VotePowerHash,
	)

	var nKeys, nPows int
	if err := row.Scan(&nKeys, &nPows); err != nil {
		return fmt.Errorf("failed to scan hash counts: %w", err)
	}

	if nKeys == 0 {
		if _, err := s.createPubKeysInTx(ctx, tx, valSet.PubKeyHash, tmconsensus.ValidatorsToPubKeys(valSet.Validators)); err != nil {
			return fmt.Errorf("failed to save new public key hash: %w", err)
		}
	}

	if nPows == 0 {
		if _, err := s.createVotePowersInTx(ctx, tx, valSet.VotePowerHash, tmconsensus.ValidatorsToVotePowers(valSet.Validators)); err != nil {
			return fmt.Errorf("failed to save new vote power hash: %w", err)
		}
	}

	// The power hashes exist.
	// Now we should be able to write the finalization record.
	res, err := tx.ExecContext(
		ctx,
		`INSERT INTO
finalizations(height, round, block_hash, validator_pub_key_hash_id, validator_power_hash_id, app_state_hash)
SELECT ?, ?, ?, keys.id, powers.id, ? FROM
(SELECT id FROM validator_pub_key_hashes WHERE hash = ?) AS keys,
(SELECT id FROM validator_power_hashes WHERE hash = ?) AS powers
`,
		height, round, []byte(blockHash), []byte(appStateHash), valSet.PubKeyHash, valSet.VotePowerHash,
	)
	if err != nil {
		if isPrimaryKeyConstraintError(err) {
			return tmstore.FinalizationOverwriteError{Height: height}
		}
		return fmt.Errorf("failed to write finalization: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to check rows affected: %w", err)
	}
	if n != 1 {
		return fmt.Errorf("expected 1 affected row, got %d", n)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

func (s *Store) LoadFinalizationByHeight(ctx context.Context, height uint64) (
	round uint32,
	blockHash string,
	valSet tmconsensus.ValidatorSet,
	appStateHash string,
	err error,
) {
	defer trace.StartRegion(ctx, "LoadFinalizationByHeight").End()

	// This two-stage query seems like a prime candidate for NextResultSet,
	// but it appears neither SQLite driver supports it.
	// So use a read-only transaction instead.
	// (Which is apparently not even enforced: see
	// https://github.com/mattn/go-sqlite3/issues/685 and
	// https://gitlab.com/cznic/sqlite/-/issues/193 .)
	tx, err := s.ro.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		err = fmt.Errorf("failed to begin read-only transaction: %w", err)
		return
	}
	defer tx.Rollback()

	// TODO: this query could also retrieve the n_validators and n_powers in order to
	// validate the hashes refer to the same number of powers and validators.
	var blockHashBytes, appStateHashBytes []byte
	var nKeys, nPowers int
	if err = tx.QueryRowContext(
		ctx,
		`SELECT
  f.round,
  f.block_hash, f.app_state_hash,
  key_hashes.hash, power_hashes.hash,
  key_hashes.n_keys, power_hashes.n_powers
FROM finalizations AS f
JOIN validator_pub_key_hashes AS key_hashes ON key_hashes.id = f.validator_pub_key_hash_id
JOIN validator_power_hashes AS power_hashes ON power_hashes.id = f.validator_power_hash_id
WHERE f.height = ?`,
		height,
	).Scan(
		&round,
		&blockHashBytes, &appStateHashBytes,
		&valSet.PubKeyHash, &valSet.VotePowerHash,
		&nKeys, &nPowers,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// The compliance tests require HeightUnknownError when no finalization matches the height.
			err = tmconsensus.HeightUnknownError{Want: height}
			return
		}

		// Otherwise, just wrap that error.
		err = fmt.Errorf("failed to scan finalization primitive data: %w", err)
		return
	}

	if nKeys != nPowers {
		// We already know they are > 0 due to checks on the respective tables.
		panic(fmt.Errorf(
			"DATABASE CORRUPTION: finalization for height %d references pubkey hash %x (with %d keys) and power hash %x (with %d powers)",
			height, valSet.PubKeyHash, nKeys, valSet.VotePowerHash, nPowers,
		))
	}

	blockHash = string(blockHashBytes)
	appStateHash = string(appStateHashBytes)

	// Then extract the validators.
	rows, err := tx.QueryContext(
		ctx,
		// It seems like there should be a view for this,
		// but maybe not because we are joining on the finalizations table?
		`SELECT keys.type, keys.key, powers.power FROM
(
  SELECT type, key, idx FROM validator_pub_keys_for_hash
    JOIN finalizations ON finalizations.validator_pub_key_hash_id = validator_pub_keys_for_hash.hash_id
    WHERE finalizations.height = ?1 ORDER BY idx ASC
) as keys
JOIN
(
  SELECT power, idx FROM validator_powers_for_hash
    JOIN finalizations ON finalizations.validator_power_hash_id = validator_powers_for_hash.hash_id
    WHERE finalizations.height = ?1 ORDER BY idx ASC
) as powers ON keys.idx = powers.idx
		`,
		height,
	)
	if err != nil {
		err = fmt.Errorf("failed to query validators for finalization: %w", err)
		return
	}
	defer rows.Close()

	var typeName string
	var keyBytes []byte
	for rows.Next() {
		keyBytes = keyBytes[:0]
		var pow uint64
		if err = rows.Scan(&typeName, &keyBytes, &pow); err != nil {
			err = fmt.Errorf("failed to scan validator row: %w", err)
			return
		}

		var key gcrypto.PubKey
		key, err = s.reg.Decode(typeName, bytes.Clone(keyBytes))
		if err != nil {
			err = fmt.Errorf("failed to decode validator key %x: %w", keyBytes, err)
			return
		}

		valSet.Validators = append(valSet.Validators, tmconsensus.Validator{
			PubKey: key,
			Power:  pow,
		})
	}

	return
}

// selectOrInsertPubKeysByHash looks up the database ID of the given pubKeyHash.
// If the record exists, the ID is returned.
// If the record does not exist, a new record is created
// for the given hash and the given public keys in the provided order.
func (s *Store) selectOrInsertPubKeysByHash(
	ctx context.Context,
	tx *sql.Tx,
	pubKeyHash []byte,
	pubKeys []gcrypto.PubKey,
) (hashID int64, err error) {
	defer trace.StartRegion(ctx, "selectOrInsertPubKeysByHash").End()

	row := tx.QueryRowContext(
		ctx,
		`SELECT id FROM validator_pub_key_hashes WHERE hash = ?`,
		pubKeyHash,
	)
	err = row.Scan(&hashID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return s.createPubKeysInTx(ctx, tx, pubKeyHash, pubKeys)
		}

		// Not sql.ErrNoRows, so nothing else we can do.
		return -1, fmt.Errorf("failed to scan pub key hash ID: %w", err)
	}

	return hashID, nil
}

// selectOrInsertVotePowersByHash looks up the database ID of the given votePowerHash.
// If the record exists, the ID is returned.
// If the record does not exist, a new record is created
// for the given hash and the given powers in the provided order.
func (s *Store) selectOrInsertVotePowersByHash(
	ctx context.Context,
	tx *sql.Tx,
	votePowerHash []byte,
	votePowers []uint64,
) (hashID int64, err error) {
	defer trace.StartRegion(ctx, "selectOrInsertVotePowersByHash").End()

	row := tx.QueryRowContext(
		ctx,
		`SELECT id FROM validator_power_hashes WHERE hash = ?`,
		votePowerHash,
	)
	err = row.Scan(&hashID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return s.createVotePowersInTx(ctx, tx, votePowerHash, votePowers)
		}

		// Not sql.ErrNoRows, so nothing else we can do.
		return -1, fmt.Errorf("failed to scan vote power hash ID: %w", err)
	}

	return hashID, nil
}

func (s *Store) SaveCommittedHeader(ctx context.Context, ch tmconsensus.CommittedHeader) error {
	defer trace.StartRegion(ctx, "SaveCommittedHeader").End()

	tx, err := s.rw.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	h := ch.Header

	headerID, err := s.createHeaderInTx(ctx, tx, h, true)
	if err != nil && !errors.As(err, new(tmstore.OverwriteError)) {
		return fmt.Errorf("failed to create header record: %w", err)
	}

	// Now store the proof.
	res, err := tx.ExecContext(
		ctx,
		`INSERT INTO commit_proofs(round, validators_pub_key_hash_id) VALUES
(?, (SELECT validators_pub_key_hash_id FROM headers WHERE id = ?))`,
		ch.Proof.Round, headerID,
	)
	if err != nil {
		return fmt.Errorf("failed to insert commit proof: %w", err)
	}
	commitProofID, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("failed to get ID of inserted commit proof: %w", err)
	}

	if err := s.saveCommitProofs(ctx, tx, commitProofID, ch.Proof.Proofs); err != nil {
		return fmt.Errorf("failed to save header's commit proof: %w", err)
	}

	// Finally, insert the committed header record.
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO committed_headers(header_id, proof_id) VALUES(?, ?)`,
		headerID, commitProofID,
	); err != nil {
		return fmt.Errorf("failed to insert into commit_headers: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction to save header: %w", err)
	}

	return nil
}

// createHeaderInTx saves a new header record
// and returns the header ID.
//
// If there is already a header with the same height and hash,
// createHeaderInTx returns the ID and a [tmstore.OverwriteError] (with Field="hash").
func (s *Store) createHeaderInTx(
	ctx context.Context,
	tx *sql.Tx,
	h tmconsensus.Header,
	committed bool,
) (int64, error) {
	defer trace.StartRegion(ctx, "createHeaderInTx").End()

	pubKeyHashID, err := s.selectOrInsertPubKeysByHash(
		ctx, tx,
		h.ValidatorSet.PubKeyHash,
		tmconsensus.ValidatorsToPubKeys(h.ValidatorSet.Validators),
	)
	if err != nil {
		return -1, fmt.Errorf("failed to save validator public keys: %w", err)
	}

	votePowerHashID, err := s.selectOrInsertVotePowersByHash(
		ctx, tx,
		h.ValidatorSet.VotePowerHash,
		tmconsensus.ValidatorsToVotePowers(h.ValidatorSet.Validators),
	)
	if err != nil {
		return -1, fmt.Errorf("failed to save validator vote powers: %w", err)
	}

	nextPubKeyHashID := pubKeyHashID
	if !bytes.Equal(h.ValidatorSet.PubKeyHash, h.NextValidatorSet.PubKeyHash) {
		nextPubKeyHashID, err = s.selectOrInsertPubKeysByHash(
			ctx, tx,
			h.NextValidatorSet.PubKeyHash,
			tmconsensus.ValidatorsToPubKeys(h.NextValidatorSet.Validators),
		)
		if err != nil {
			return -1, fmt.Errorf("failed to save next validator public keys: %w", err)
		}
	}

	nextVotePowerHashID := votePowerHashID
	if !bytes.Equal(h.ValidatorSet.VotePowerHash, h.NextValidatorSet.VotePowerHash) {
		nextVotePowerHashID, err = s.selectOrInsertVotePowersByHash(
			ctx, tx,
			h.NextValidatorSet.VotePowerHash,
			tmconsensus.ValidatorsToVotePowers(h.NextValidatorSet.Validators),
		)
		if err != nil {
			return -1, fmt.Errorf("failed to save next validator public keys: %w", err)
		}
	}

	// Insert the previous commit proof, if one exists.
	var prevCommitProofID sql.NullInt64
	if h.PrevCommitProof.PubKeyHash != "" {
		res, err := tx.ExecContext(
			ctx,
			`INSERT INTO commit_proofs(round, validators_pub_key_hash_id) VALUES
(?, (SELECT id FROM validator_pub_key_hashes WHERE hash = ?))`,
			h.PrevCommitProof.Round, []byte(h.PrevCommitProof.PubKeyHash),
		)
		if err != nil {
			return -1, fmt.Errorf("failed to create prev commit proof row: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return -1, fmt.Errorf("failed to get ID of prev commit proof's row: %w", err)
		}
		prevCommitProofID = sql.NullInt64{Int64: id, Valid: true}

		if err := s.saveCommitProofs(ctx, tx, id, h.PrevCommitProof.Proofs); err != nil {
			return -1, fmt.Errorf("failed to save previous commit proofs: %w", err)
		}
	}

	res, err := tx.ExecContext(
		ctx,
		`INSERT INTO headers(
hash, prev_block_hash, height,
validators_pub_key_hash_id,
validators_power_hash_id,
next_validators_pub_key_hash_id,
next_validators_power_hash_id,
prev_commit_proof_id,
data_id, prev_app_state_hash,
user_annotations, driver_annotations,
committed
) VALUES (
$hash, $prev_block_hash, $height,
$pub_key_hash_id,
$vote_power_hash_id,
$next_pub_key_hash_id,
$next_vote_power_hash_id,
$prev_commit_proof_id,
$data_id, $prev_app_state_hash,
$user_annotations, $driver_annotations,
$committed)`,
		sql.Named("hash", h.Hash), sql.Named("prev_block_hash", h.PrevBlockHash), sql.Named("height", h.Height),
		sql.Named("pub_key_hash_id", pubKeyHashID),
		sql.Named("prev_commit_proof_id", prevCommitProofID),
		sql.Named("data_id", h.DataID), sql.Named("prev_app_state_hash", h.PrevAppStateHash),
		sql.Named("user_annotations", h.Annotations.User), sql.Named("driver_annotations", h.Annotations.Driver),
		sql.Named("pub_key_hash_id", pubKeyHashID),
		sql.Named("vote_power_hash_id", votePowerHashID),
		sql.Named("next_pub_key_hash_id", nextPubKeyHashID),
		sql.Named("next_vote_power_hash_id", nextVotePowerHashID),
		sql.Named("committed", committed),
	)
	if err != nil {
		if isUniqueConstraintError(err) {
			// Special case for compliance.
			// There is only one unique constraint on the headers table.
			overwriteErr := tmstore.OverwriteError{
				Field: "hash",
				Value: fmt.Sprintf("%x", h.Hash),
			}

			// But, we still need to get the underlying ID.
			// There are two possible situations here.
			// If we are not setting the committed flag,
			// we can simply retrieve the existing ID.
			// But if we are setting the committed flag,
			// which can never be unset,
			// then we just do an update returning statement.
			var id int64
			if committed {
				if err := tx.QueryRowContext(
					ctx,
					`UPDATE headers SET committed=1 WHERE hash = ? RETURNING id`,
					h.Hash,
				).Scan(&id); err != nil {
					return -1, fmt.Errorf("failed to set committed flag and get ID for header: %w", err)
				}
			} else {
				if err := tx.QueryRowContext(
					ctx,
					`SELECT id FROM headers WHERE hash = ?`,
					h.Hash,
				).Scan(&id); err != nil {
					return -1, fmt.Errorf("failed to fall back to ID retrieval for header: %w", err)
				}
			}

			return id, overwriteErr
		}

		return -1, fmt.Errorf("failed to store header: %w", err)
	}

	// Need the header ID for the committed_headers insert coming up.
	headerID, err := res.LastInsertId()
	if err != nil {
		return -1, fmt.Errorf("failed to get ID of inserted header: %w", err)
	}

	return headerID, nil
}

// saveCommitProofs accepts a commitProofID and the sparse proof to store,
// and creates all the child entries for the commit proof.
// This is applicable to both a header's previous commit proof
// or to a subjective commit proof for a header.
func (s *Store) saveCommitProofs(
	ctx context.Context,
	tx *sql.Tx,
	commitProofID int64,
	proofs map[string][]gcrypto.SparseSignature,
) error {
	defer trace.StartRegion(ctx, "saveCommitProofs").End()

	// First create the voted blocks.
	q := `INSERT INTO commit_proof_voted_blocks(commit_proof_id, block_hash) VALUES (?,?)` +
		// We can safely assume there is at least one proof,
		// which simplifies the comma joining.
		strings.Repeat(", (?,?)", len(proofs)-1) +
		// We iterate the map in arbitrary order,
		// so it's simpler to just get back the hash-ID pairings.
		// Plus, this is the only safe way to use RETURNING,
		// which is defined to report back results in arbitrary order.
		` RETURNING id, block_hash`
	args := make([]any, 0, 2*len(proofs))
	for hash := range proofs {
		args = append(args, commitProofID, []byte(hash))
	}
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("failed to insert voted blocks: %w", err)
	}
	defer rows.Close()

	// Possible earlier GC.
	q = ""
	clear(args)

	// Keep a running map of database details.
	type proofTemp struct {
		votedBlockID int64
		sigIDs       []int64
	}
	votedBlocks := make(map[string]proofTemp, len(proofs))

	var tempBlockHash []byte
	for rows.Next() {
		tempBlockHash = tempBlockHash[:0]
		var pt proofTemp
		if err := rows.Scan(&pt.votedBlockID, &tempBlockHash); err != nil {
			return fmt.Errorf("failed to scan voted block ID: %w", err)
		}
		votedBlocks[string(tempBlockHash)] = pt
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("failure when scanning result from inserting voted blocks: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("failed to close result rows from inserting voted blocks: %w", err)
	}

	// Now insert the sparse signatures.
	for blockHash, sigs := range proofs {
		// Insert all the signatures by their block hash.
		// This is easier to manage mapping the returned IDs,
		// compared to doing one bulk insert.
		q = `INSERT INTO sparse_signatures(key_id, signature) VALUES (?,?)` +
			strings.Repeat(", (?,?)", len(sigs)-1) +
			` RETURNING id`

		// We created 2 args per proof before, and we are doing it again now,
		// so this shouldn't grow the args slice.
		args = args[:0]
		for _, sig := range sigs {
			args = append(args, sig.KeyID, sig.Sig)
		}

		rows, err := tx.QueryContext(ctx, q, args...)
		if err != nil {
			return fmt.Errorf("failed to insert sparse signatures: %w", err)
		}
		defer rows.Close() // TODO: this loop could move to a new method, to avoid this looped defer.

		sigIDs := make([]int64, 0, len(sigs))
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return fmt.Errorf("failed to scan sparse signature ID: %w", err)
			}
			sigIDs = append(sigIDs, id)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("error iterating sparse signatures: %w", err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("error closing sparse signature iterator: %w", err)
		}

		pt := votedBlocks[blockHash]
		pt.sigIDs = sigIDs
		votedBlocks[blockHash] = pt
	}

	// Store the relationship between the voted blocks and the signatures.
	nSigs := 0
	for _, pt := range votedBlocks {
		nSigs += len(pt.sigIDs)
	}

	if cap(args) < 2*nSigs {
		args = make([]any, 0, 2*nSigs)
	} else {
		clear(args)
		args = args[:0]
	}

	q = `INSERT INTO commit_proof_block_signatures(
commit_proof_voted_block_id, sparse_signature_id
) VALUES (?,?)` +
		strings.Repeat(", (?,?)", nSigs-1)
	for _, pt := range votedBlocks {
		for _, sigID := range pt.sigIDs {
			args = append(args, pt.votedBlockID, sigID)
		}
	}

	if _, err := tx.ExecContext(ctx, q, args...); err != nil {
		return fmt.Errorf("failed to insert into commit_proof_block_signatures: %w", err)
	}

	return nil
}

func (s *Store) LoadCommittedHeader(ctx context.Context, height uint64) (tmconsensus.CommittedHeader, error) {
	defer trace.StartRegion(ctx, "LoadCommittedHeader").End()

	tx, err := s.ro.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return tmconsensus.CommittedHeader{}, fmt.Errorf(
			"failed to begin read-only transaction: %w", err,
		)
	}
	defer tx.Rollback()

	h, headerID, err := s.loadCommittedHeaderByHeight(ctx, tx, height)
	if err != nil {
		return tmconsensus.CommittedHeader{}, fmt.Errorf(
			"failed to load header with height %d: %w", height, err,
		)
	}

	// First get the outer commit proof value.
	// TODO: there is probably a way to do this in a single query.
	var commitProofID int64
	proof := tmconsensus.CommitProof{
		PubKeyHash: string(h.ValidatorSet.PubKeyHash),
		Proofs:     map[string][]gcrypto.SparseSignature{},
	}
	if err := tx.QueryRowContext(
		ctx,
		`SELECT commit_proofs.id, round FROM commit_proofs
JOIN committed_headers ON committed_headers.proof_id = commit_proofs.id
WHERE committed_headers.header_id = ?`,
		headerID,
	).Scan(&commitProofID, &proof.Round); err != nil {
		return tmconsensus.CommittedHeader{}, fmt.Errorf(
			"failed to retrieve commit proof: %w", err,
		)
	}

	// Use the commit proof ID to get the actual signatures.
	rows, err := tx.QueryContext(
		ctx,
		`SELECT block_hash, key_id, signature FROM proof_signatures
WHERE commit_proof_id = ?
ORDER BY key_id`, // Order not strictly necessary, but convenient for tests.
		commitProofID,
	)
	if err != nil {
		return tmconsensus.CommittedHeader{}, fmt.Errorf(
			"failed to retrieve signatures: %w", err,
		)
	}
	defer rows.Close()

	var blockHash, keyID, sig []byte
	for rows.Next() {
		// Reset and reuse each slice,
		// so the destinations are reused repeatedly.
		// We're going to allocate when we create the SparseSignature anyway,
		// but those will be right-sized at creation.
		blockHash = blockHash[:0]
		keyID = keyID[:0]
		sig = sig[:0]
		if err := rows.Scan(&blockHash, &keyID, &sig); err != nil {
			return tmconsensus.CommittedHeader{}, fmt.Errorf(
				"failed to scan signature row: %w", err,
			)
		}
		proof.Proofs[string(blockHash)] = append(
			proof.Proofs[string(blockHash)],
			gcrypto.SparseSignature{
				KeyID: bytes.Clone(keyID),
				Sig:   bytes.Clone(sig),
			},
		)
	}
	if err := rows.Err(); err != nil {
		return tmconsensus.CommittedHeader{}, fmt.Errorf(
			"failed to iterate signature rows: %w", err,
		)
	}

	return tmconsensus.CommittedHeader{
		Header: h,
		Proof:  proof,
	}, nil
}

func (s *Store) loadHeaderByID(ctx context.Context, tx *sql.Tx, id int64) (
	tmconsensus.Header, error,
) {
	defer trace.StartRegion(ctx, "loadHeaderByID").End()

	h, _, err := s.loadHeaderDynamic(ctx, tx, `id = ?`, id)
	return h, err
}

func (s *Store) loadCommittedHeaderByHeight(
	ctx context.Context,
	tx *sql.Tx,
	height uint64,
) (tmconsensus.Header, int64, error) {
	defer trace.StartRegion(ctx, "loadCommittedHeaderByHeight").End()

	h, id, err := s.loadHeaderDynamic(ctx, tx, `committed = 1 AND height = ?`, height)
	if errors.Is(err, sql.ErrNoRows) {
		// Special case for compliance.
		return h, id, tmconsensus.HeightUnknownError{Want: height}
	}

	return h, id, err
}

func (s *Store) loadHeaderDynamic(
	ctx context.Context,
	tx *sql.Tx,
	where string,
	queryArgs ...any,
) (tmconsensus.Header, int64, error) {
	defer trace.StartRegion(ctx, "loadHeaderDynamic").End()

	var h tmconsensus.Header
	var headerID, pkhID, npkhID, vphID, nvphID int64
	var pcpID sql.NullInt64
	if err := tx.QueryRowContext(
		ctx,
		`SELECT
id,
height,
hash, prev_block_hash,
data_id, prev_app_state_hash,
user_annotations, driver_annotations,
prev_commit_proof_id,
validators_pub_key_hash_id, next_validators_pub_key_hash_id,
validators_power_hash_id, next_validators_power_hash_id
FROM headers
WHERE
`+where, // Dynamic where here, but you can see the callers are hardcoded to use bound arguments.
		queryArgs...,
	).Scan(
		&headerID,
		&h.Height,
		&h.Hash, &h.PrevBlockHash,
		&h.DataID, &h.PrevAppStateHash,
		&h.Annotations.User, &h.Annotations.Driver,
		&pcpID,
		&pkhID, &npkhID,
		&vphID, &nvphID,
	); err != nil {
		return tmconsensus.Header{}, headerID, fmt.Errorf(
			"failed to scan header values from database: %w", err,
		)
	}

	// Assuming for now that it's better to do an extra query to get the hashes,
	// since we don't know up front whether the current and next validator sets
	// have the same hashes.
	var nvs, nnvs, nps, nnps int
	if err := tx.QueryRowContext(
		ctx,
		`SELECT * FROM
(SELECT hash, n_keys FROM validator_pub_key_hashes WHERE id = ?),
(SELECT hash, n_keys FROM validator_pub_key_hashes WHERE id = ?),
(SELECT hash, n_powers FROM validator_power_hashes WHERE id = ?),
(SELECT hash, n_powers FROM validator_power_hashes WHERE id = ?)`,
		pkhID, npkhID,
		vphID, nvphID,
	).Scan(
		&h.ValidatorSet.PubKeyHash, &nvs,
		&h.NextValidatorSet.PubKeyHash, &nnvs,
		&h.ValidatorSet.VotePowerHash, &nps,
		&h.NextValidatorSet.VotePowerHash, &nnps,
	); err != nil {
		return tmconsensus.Header{}, headerID, fmt.Errorf(
			"failed to scan validator hashes from database: %w", err,
		)
	}

	if nvs != nps {
		panic(fmt.Errorf(
			"DATABASE CORRUPTION: attempted to load header at height %d with val hash %x/%d, pow hash %x/%d",
			h.Height,
			h.ValidatorSet.PubKeyHash, nvs,
			h.ValidatorSet.VotePowerHash, nps,
		))
	}
	if nnvs != nnps {
		panic(fmt.Errorf(
			"DATABASE CORRUPTION: attempted to load header at height %d with next val hash %x/%d, next pow hash %x/%d",
			h.Height,
			h.NextValidatorSet.PubKeyHash, nnvs,
			h.NextValidatorSet.VotePowerHash, nnps,
		))
	}

	h.ValidatorSet.Validators = make([]tmconsensus.Validator, nvs)

	// Now we can get the validator public keys.
	rows, err := tx.QueryContext(
		ctx,
		`SELECT idx, type, key FROM validator_pub_keys_for_hash WHERE hash_id = ?`,
		pkhID,
	)
	if err != nil {
		return tmconsensus.Header{}, headerID, fmt.Errorf(
			"failed to query validator public keys: %w", err,
		)
	}
	defer rows.Close()

	var typeName string
	var keyBytes []byte
	for rows.Next() {
		keyBytes = keyBytes[:0]
		var idx int
		if err := rows.Scan(&idx, &typeName, &keyBytes); err != nil {
			return tmconsensus.Header{}, headerID, fmt.Errorf(
				"failed to scan validator public key: %w", err,
			)
		}
		key, err := s.reg.Decode(typeName, bytes.Clone(keyBytes))
		if err != nil {
			return tmconsensus.Header{}, headerID, fmt.Errorf(
				"failed to decode validator key %x: %w", keyBytes, err,
			)
		}

		h.ValidatorSet.Validators[idx].PubKey = key
	}
	if rows.Err() != nil {
		return tmconsensus.Header{}, headerID, fmt.Errorf(
			"failed to iterate validator keys: %w", rows.Err(),
		)
	}
	_ = rows.Close()

	// Same for the powers.
	rows, err = tx.QueryContext(
		ctx,
		`SELECT idx, power FROM validator_powers_for_hash WHERE hash_id = ?`,
		vphID,
	)
	if err != nil {
		return tmconsensus.Header{}, headerID, fmt.Errorf(
			"failed to query validator powers: %w", err,
		)
	}
	defer rows.Close()
	for rows.Next() {
		var idx int
		var pow uint64
		if err := rows.Scan(&idx, &pow); err != nil {
			return tmconsensus.Header{}, headerID, fmt.Errorf(
				"failed to scan validator power: %w", err,
			)
		}

		h.ValidatorSet.Validators[idx].Power = pow
	}
	if rows.Err() != nil {
		return tmconsensus.Header{}, headerID, fmt.Errorf(
			"failed to iterate validator powers: %w", rows.Err(),
		)
	}
	if err := rows.Close(); err != nil {
		return tmconsensus.Header{}, headerID, fmt.Errorf(
			"failed to close validator power iterator: %w", rows.Err(),
		)
	}

	if bytes.Equal(h.ValidatorSet.PubKeyHash, h.NextValidatorSet.PubKeyHash) &&
		bytes.Equal(h.ValidatorSet.VotePowerHash, h.NextValidatorSet.VotePowerHash) {
		h.NextValidatorSet.Validators = slices.Clone(h.ValidatorSet.Validators)
	} else {
		panic("TODO: handle different next validator set")
	}

	if pcpID.Valid {
		// Populate the previous commit proof.

		// TODO: We might be able to do this query as a join on the earlier header query?
		pubKeyHash := make([]byte, 0, len(h.ValidatorSet.PubKeyHash))
		if err := tx.QueryRowContext(
			ctx,
			`SELECT round, hashes.hash FROM commit_proofs
JOIN validator_pub_key_hashes AS hashes ON hashes.id = commit_proofs.validators_pub_key_hash_id
WHERE commit_proofs.id = ?`,
			pcpID.Int64,
		).Scan(&h.PrevCommitProof.Round, &pubKeyHash); err != nil {
			return tmconsensus.Header{}, headerID, fmt.Errorf(
				"failed to retrieve scan previous commit round and pub key hash: %w", err,
			)
		}
		h.PrevCommitProof.PubKeyHash = string(pubKeyHash)

		rows, err := tx.QueryContext(
			ctx,
			`SELECT block_hash, key_id, signature FROM proof_signatures
WHERE commit_proof_id = ?
ORDER BY key_id`, // Order not strictly necessary, but convenient for tests.
			pcpID.Int64,
		)
		if err != nil {
			return tmconsensus.Header{}, headerID, fmt.Errorf(
				"failed to retrieve previous commit proof signatures: %w", err,
			)
		}
		defer rows.Close()

		// We are going to populate the previous commit proof map.
		// We are assuming that most of the time, there are only votes for the main block,
		// and maybe some votes for nil.
		h.PrevCommitProof.Proofs = make(map[string][]gcrypto.SparseSignature, 2)

		var blockHash, keyID, sig []byte
		for rows.Next() {
			// Reset and reuse each slice,
			// so the destinations are reused repeatedly.
			// We're going to allocate when we create the SparseSignature anyway,
			// but those will be right-sized at creation.
			blockHash = blockHash[:0]
			keyID = keyID[:0]
			sig = sig[:0]
			if err := rows.Scan(&blockHash, &keyID, &sig); err != nil {
				return tmconsensus.Header{}, headerID, fmt.Errorf(
					"failed to scan signature row: %w", err,
				)
			}
			h.PrevCommitProof.Proofs[string(blockHash)] = append(
				h.PrevCommitProof.Proofs[string(blockHash)],
				gcrypto.SparseSignature{
					KeyID: bytes.Clone(keyID),
					Sig:   bytes.Clone(sig),
				},
			)
		}

		if err := rows.Close(); err != nil {
			return tmconsensus.Header{}, headerID, fmt.Errorf(
				"failed to close previous commit proof signatures iterator: %w", err,
			)
		}
	}

	return h, headerID, nil
}

func (s *Store) SaveProposedHeaderAction(ctx context.Context, ph tmconsensus.ProposedHeader) error {
	defer trace.StartRegion(ctx, "SaveProposedHeaderAction").End()

	tx, err := s.rw.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	headerID, err := s.createHeaderInTx(ctx, tx, ph.Header, false)
	if err != nil && !errors.As(err, new(tmstore.OverwriteError)) {
		// We ignore the attempted overwrite and proceed with the proposed header insertion.
		return fmt.Errorf("failed to create header: %w", err)
	}

	// We are assuming that our proposer's public key is already in the validators list.
	// If it isn't, we should not be proposing a block.
	res, err := tx.ExecContext(
		ctx,
		`INSERT INTO proposed_headers(
header_id, height, round,
user_annotations, driver_annotations,
signature,
proposer_pub_key_id
) VALUES (
?, ?, ?,
?, ?,
?,
(SELECT id FROM validator_pub_keys WHERE type = ? AND key = ?)
)`,
		headerID, ph.Header.Height, ph.Round,
		ph.Annotations.User, ph.Annotations.Driver,
		ph.Signature,
		ph.ProposerPubKey.TypeName(), ph.ProposerPubKey.PubKeyBytes(),
	)
	if err != nil {
		if isUniqueConstraintError(err) {
			// Special case for action store compliance.
			// There is only one unique constraint on the proposed headers table.
			return tmstore.DoubleActionError{Type: "proposed block"}
		}
		return fmt.Errorf("failed to save proposed header: %w", err)
	}

	phID, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("failed to get insert ID from creating proposed header: %w", err)
	}

	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO actions_proposed_headers(height, round, proposed_header_id) VALUES(?, ?, ?)`,
		ph.Header.Height, ph.Round, phID,
	)
	if err != nil {
		if isUniqueConstraintError(err) {
			// Special case for action store compliance.
			// There is only one unique constraint that we can have violated here.
			// But, we probably can never reach this, as the proposed_headers unique constraint
			// should have failed before we got here.
			return tmstore.DoubleActionError{Type: "proposed block"}
		}

		return fmt.Errorf("failed to save proposed header action: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit saving proposed header: %w", err)
	}

	return nil
}

func (s *Store) SavePrevoteAction(
	ctx context.Context,
	pubKey gcrypto.PubKey,
	vt tmconsensus.VoteTarget,
	sig []byte,
) error {
	defer trace.StartRegion(ctx, "SavePrevoteAction").End()

	return s.saveVote(ctx, pubKey, vt, sig, voteEntryConfig{
		IntoTable:  "actions_prevotes",
		ActionType: "prevote",
		OtherTable: "actions_precommits",
	})
}

func (s *Store) SavePrecommitAction(
	ctx context.Context,
	pubKey gcrypto.PubKey,
	vt tmconsensus.VoteTarget,
	sig []byte,
) error {
	defer trace.StartRegion(ctx, "SavePrecommitAction").End()

	return s.saveVote(ctx, pubKey, vt, sig, voteEntryConfig{
		IntoTable:  "actions_precommits",
		ActionType: "precommit",
		OtherTable: "actions_prevotes",
	})
}

type voteEntryConfig struct {
	IntoTable  string
	ActionType string
	OtherTable string
}

func (s *Store) saveVote(
	ctx context.Context,
	pubKey gcrypto.PubKey,
	vt tmconsensus.VoteTarget,
	sig []byte,
	vec voteEntryConfig,
) error {
	defer trace.StartRegion(ctx, "saveVote").End()

	switch vec.IntoTable {
	case "actions_prevotes", "actions_precommits":
		// Okay.
	default:
		panic(fmt.Errorf("BUG: illegal IntoTable name %s for saveVote", vec.IntoTable))
	}

	var blockHash []byte
	if vt.BlockHash != "" {
		blockHash = []byte(vt.BlockHash)
	}
	if _, err := s.rw.ExecContext(
		ctx,
		// Yes, we are doing string interpolation in this query,
		// but there are only two calls into this method,
		// and they both use hardcoded values.
		`INSERT INTO `+vec.IntoTable+`(
height, round,
block_hash,
signature,
signer_pub_key_id
) VALUES(
$height, $round,
$block_hash, $signature,
(
CASE WHEN EXISTS (
  SELECT 1 FROM `+vec.OtherTable+` WHERE
  height = $height AND round = $round AND signer_pub_key_id != (
    SELECT id FROM validator_pub_keys WHERE type = $type AND key = $key
  )
) THEN NULL
ELSE
  (SELECT id FROM validator_pub_keys WHERE type = $type AND key = $key)
END
)
)`,
		sql.Named("height", vt.Height),
		sql.Named("round", vt.Round),
		sql.Named("block_hash", blockHash),
		sql.Named("signature", sig),
		sql.Named("type", pubKey.TypeName()), sql.Named("key", pubKey.PubKeyBytes()),
	); err != nil {
		if isUniqueConstraintError(err) {
			// Special case for action store compliance.
			// There is only one unique constraint that we can have violated here.
			return tmstore.DoubleActionError{Type: vec.ActionType}
		}
		if isNotNullConstraintError(err) {
			// Another special case for compliance.
			// We were able to do all the prior work outside of a transaction,
			// and this is such an exceptional case,
			// we will do this also outside of a transaction.
			var typeName string
			var haveKeyBytes []byte
			if err := s.ro.QueryRowContext(
				ctx,
				`SELECT type, key FROM validator_pub_keys WHERE id = (
  SELECT signer_pub_key_id FROM `+vec.OtherTable+` WHERE height = ? AND round = ?
)`,
				vt.Height, vt.Round,
			).Scan(&typeName, &haveKeyBytes); err != nil {
				return fmt.Errorf("failed to query key conflict: %w", err)
			}

			// This Decode call doesn't need a bytes.Clone,
			// since we are not reusing haveKeyBytes like in other calls in this file.
			haveKey, err := s.reg.Decode(typeName, haveKeyBytes)
			if err != nil {
				return fmt.Errorf("failed to decode existing conflicting key: %w", err)
			}

			return tmstore.PubKeyChangedError{
				ActionType: vec.ActionType,
				Want:       string(haveKey.PubKeyBytes()),
				Got:        string(pubKey.PubKeyBytes()),
			}
		}
		return fmt.Errorf("failed to insert vote to %s: %w", vec.IntoTable, err)
	}

	return nil
}

func (s *Store) LoadActions(ctx context.Context, height uint64, round uint32) (tmstore.RoundActions, error) {
	defer trace.StartRegion(ctx, "LoadActions").End()

	// LoadActions should only be called once at application startup,
	// so it's not a big deal if we take several queries to get all the action details.
	tx, err := s.ro.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return tmstore.RoundActions{}, fmt.Errorf(
			"failed to begin transaction: %w", err,
		)
	}
	defer tx.Rollback()

	var phID, prevoteKeyID, precommitKeyID sql.NullInt64
	var prevoteHash, prevoteSig, precommitHash, precommitSig []byte
	if err := tx.QueryRowContext(
		ctx,
		`SELECT
ph_id,
pv_block_hash, pv_signature, pv_key_id,
pc_block_hash, pc_signature, pc_key_id
FROM actions
WHERE height = ? AND round = ?`,
		height, round,
	).Scan(
		&phID,
		&prevoteHash, &prevoteSig, &prevoteKeyID,
		&precommitHash, &precommitSig, &precommitKeyID,
	); err != nil {
		if err == sql.ErrNoRows {
			// Special case for interface compliance.
			return tmstore.RoundActions{}, tmconsensus.RoundUnknownError{
				WantHeight: height,
				WantRound:  round,
			}
		}

		return tmstore.RoundActions{}, fmt.Errorf(
			"failed to query actions: %w", err,
		)
	}

	if prevoteKeyID.Valid && precommitKeyID.Valid && prevoteKeyID.Int64 != precommitKeyID.Int64 {
		panic(fmt.Errorf(
			"DATABASE CORRUPTION: got different key IDs %d and %d for prevote and precommit at height=%d round=%d",
			prevoteKeyID.Int64, precommitKeyID.Int64, height, round,
		))
	}

	// Next, load the proposed header metadata.
	var proposerKeyID sql.NullInt64
	var ph tmconsensus.ProposedHeader
	if phID.Valid {
		var headerID int64
		var phRound uint32
		var userAnnotations, driverAnnotations, sig []byte
		if err := tx.QueryRowContext(
			ctx,
			`SELECT
header_id,
round, proposer_pub_key_id,
user_annotations, driver_annotations,
signature
FROM proposed_headers WHERE id = ?`,
			phID,
		).Scan(
			&headerID,
			&phRound, &proposerKeyID,
			&userAnnotations, &driverAnnotations,
			&sig,
		); err != nil {
			// TODO: gracefully handle not having a proposed block.
			return tmstore.RoundActions{}, fmt.Errorf(
				"failed to query proposed headers: %w", err,
			)
		}

		if phRound != round {
			panic(fmt.Errorf(
				"DATABASE CORRUPTION: found proposed header at round %d when requesting height=%d round=%d",
				phRound, height, round,
			))
		}

		if proposerKeyID.Valid &&
			prevoteKeyID.Valid && proposerKeyID.Int64 != prevoteKeyID.Int64 {
			panic(fmt.Errorf(
				"DATABASE CORRUPTION: got different key IDs %d and %d for proposer and prevote at height=%d round=%d",
				proposerKeyID.Int64, prevoteKeyID.Int64, height, round,
			))
		}
		if proposerKeyID.Valid &&
			precommitKeyID.Valid && proposerKeyID.Int64 != precommitKeyID.Int64 {
			panic(fmt.Errorf(
				"DATABASE CORRUPTION: got different key IDs %d and %d for proposer and prevote at height=%d round=%d",
				proposerKeyID.Int64, precommitKeyID.Int64, height, round,
			))
		}

		h, err := s.loadHeaderByID(ctx, tx, headerID)
		if err != nil {
			return tmstore.RoundActions{}, fmt.Errorf(
				"failed to load header by ID (%d): %w", headerID, err,
			)
		}

		ph = tmconsensus.ProposedHeader{
			Header: h,
			Round:  round,

			// We don't have the proposer pub key yet --
			// we are about to load the key after this branch.

			Annotations: tmconsensus.Annotations{
				User:   userAnnotations,
				Driver: driverAnnotations,
			},

			Signature: sig,
		}
	}

	// We must have had at least one action.
	var keyID int64
	switch {
	case proposerKeyID.Valid:
		keyID = proposerKeyID.Int64
	case prevoteKeyID.Valid:
		keyID = prevoteKeyID.Int64
	case precommitKeyID.Valid:
		keyID = precommitKeyID.Int64
	default:
		panic(fmt.Errorf(
			"BUG: attempting to look up unknown key at height=%d round=%d",
			height, round,
		))
	}

	var typeName string
	var keyBytes []byte
	if err := tx.QueryRowContext(
		ctx,
		`SELECT type, key FROM validator_pub_keys WHERE id = ?`,
		keyID,
	).Scan(&typeName, &keyBytes); err != nil {
		return tmstore.RoundActions{}, fmt.Errorf(
			"failed to scan public key: %w", err,
		)
	}

	// This Decode call doesn't need a bytes.Clone,
	// since we are not reusing haveKeyBytes like in other calls in this file.
	key, err := s.reg.Decode(typeName, keyBytes)
	if err != nil {
		return tmstore.RoundActions{}, fmt.Errorf(
			"failed to decode validator key %x: %w", keyBytes, err,
		)
	}

	ra := tmstore.RoundActions{
		Height: height,
		Round:  round,

		ProposedHeader: ph,

		PubKey: key,

		PrevoteTarget:      string(prevoteHash),
		PrevoteSignature:   string(prevoteSig),
		PrecommitTarget:    string(precommitHash),
		PrecommitSignature: string(precommitSig),
	}

	if len(ra.ProposedHeader.Signature) > 0 {
		ra.ProposedHeader.ProposerPubKey = key
	}

	return ra, nil
}

func (s *Store) SaveRoundProposedHeader(ctx context.Context, ph tmconsensus.ProposedHeader) error {
	defer trace.StartRegion(ctx, "SaveRoundProposedHeader").End()

	tx, err := s.rw.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	headerID, err := s.createHeaderInTx(ctx, tx, ph.Header, false)
	if err != nil && !errors.As(err, new(tmstore.OverwriteError)) {
		return fmt.Errorf("failed to create header: %w", err)
	}

	// We are assuming that our proposer's public key is already in the validators list.
	// If it isn't, we should not be accepting their proposed header.
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO proposed_headers(
header_id, height, round,
user_annotations, driver_annotations,
signature,
proposer_pub_key_id
) VALUES (
?, ?, ?,
?, ?,
?,
(SELECT id FROM validator_pub_keys WHERE type = ? AND key = ?)
)`,
		headerID, ph.Header.Height, ph.Round,
		ph.Annotations.User, ph.Annotations.Driver,
		ph.Signature,
		ph.ProposerPubKey.TypeName(), ph.ProposerPubKey.PubKeyBytes(),
	); err != nil {
		if isUniqueConstraintError(err) {
			// This is a special case for compliance tests.
			// There is only one unique constraint across height/round/pubkey.
			// If we hit the constraint, it was because we tried to save a proposed header
			// at the same height and round for the one public key.
			return tmstore.OverwriteError{
				Field: "pubkey",
				Value: fmt.Sprintf("%x", ph.ProposerPubKey.PubKeyBytes()),
			}
		}
		return fmt.Errorf("failed to save proposed header: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

func (s *Store) SaveRoundReplayedHeader(ctx context.Context, h tmconsensus.Header) error {
	defer trace.StartRegion(ctx, "SaveRoundReplayedHeader").End()

	tx, err := s.rw.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := s.createHeaderInTx(ctx, tx, h, false); err != nil {
		return fmt.Errorf("failed to save replayed header: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

func (s *Store) OverwriteRoundPrevoteProofs(
	ctx context.Context,
	height uint64,
	round uint32,
	proofs tmconsensus.SparseSignatureCollection,
) error {
	defer trace.StartRegion(ctx, "OverwriteRoundPrevoteProofs").End()

	return s.overwriteRoundProofs(
		ctx, height, round, proofs, "prevote",
	)
}

func (s *Store) OverwriteRoundPrecommitProofs(
	ctx context.Context,
	height uint64,
	round uint32,
	proofs tmconsensus.SparseSignatureCollection,
) error {
	defer trace.StartRegion(ctx, "OverwriteRoundPrecommitProofs").End()

	return s.overwriteRoundProofs(
		ctx, height, round, proofs, "precommit",
	)
}

func (s *Store) overwriteRoundProofs(
	ctx context.Context,
	height uint64,
	round uint32,
	proofs tmconsensus.SparseSignatureCollection,
	voteType string,
) error {
	defer trace.StartRegion(ctx, "overwriteRoundProofs").End()

	switch voteType {
	case "prevote", "precommit":
		// Okay.
	default:
		panic(fmt.Errorf(
			"BUG: illegal voteType %s in overwriteRoundProofs", voteType,
		))
	}

	tx, err := s.rw.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// TODO: delete superseded signatures; tests need updated first.

	// TODO: gassert: all key IDs in proofs are unique.

	// TODO instead: select the joined result,
	// then don't overwrite anything that didn't exist.

	// For now, do three insert attempts to cover everything here.
	// First, create or get the ID of the round_votes row.
	var roundVoteID int64
	if err := tx.QueryRowContext(
		ctx,
		// Hacky way to attempt an insert
		// and return either the new or existing ID.
		// This RETURNING is fine since we know we only touch one record.
		`INSERT INTO round_votes(height, round, validators_pub_key_hash_id)
VALUES (
?, ?,
(SELECT id FROM validator_pub_key_hashes WHERE hash = ?)
) ON CONFLICT DO UPDATE SET id=id RETURNING id`,
		height, round,
		proofs.PubKeyHash,
	).Scan(&roundVoteID); err != nil {
		return fmt.Errorf("failed to get round vote ID: %w", err)
	}

	type bhid struct {
		Hash []byte
		ID   int64
	}
	blockHashesAndIDs := make([]bhid, 0, len(proofs.BlockSignatures))
	args := make([]any, 0, 2*len(proofs.BlockSignatures))
	for blockHash := range proofs.BlockSignatures {
		var x bhid
		if blockHash != "" {
			x.Hash = []byte(blockHash)
		}
		blockHashesAndIDs = append(blockHashesAndIDs, x)
		args = append(args, x.Hash, roundVoteID)
	}

	// Now we have our block hashes, in an arbitrary order.
	// Do another hacky insert to ensure all the block records exist.
	rows, err := tx.QueryContext(
		ctx,
		`INSERT INTO round_`+voteType+`_blocks(block_hash, round_vote_id) VALUES (?, ?) `+
			strings.Repeat(", (?,?)", len(blockHashesAndIDs)-1)+
			// TODO: RETURNING id is insufficient:
			// https://sqlite.org/lang_returning.html#limitations_and_caveats
			// "The rows emitted by the RETURNING clause appear in an arbitrary order. ...
			// That order might change depending on the database schema,
			// upon the specific release of SQLite used,
			// or even from one execution of the same statement to the next.
			// There is no way to cause the output rows to appear in a particular order."
			// So we need to also return the block_hash.
			` ON CONFLICT DO UPDATE SET id=id RETURNING id`,
		args...,
	)
	if err != nil {
		return fmt.Errorf("failed to get %s block IDs: %w", voteType, err)
	}
	defer rows.Close()

	var i int
	for rows.Next() {
		if err := rows.Scan(&blockHashesAndIDs[i].ID); err != nil {
			return fmt.Errorf("failed to scan %s block ID: %w", voteType, err)
		}
		i++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("failed to iterate %s block IDs: %w", voteType, err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("failed to close %s block ID iterator: %w", voteType, err)
	}
	if i != len(blockHashesAndIDs) {
		panic(fmt.Errorf(
			"IMPOSSIBLE: expected %d blocks but got %d", len(blockHashesAndIDs), i,
		))
	}

	// Now our last bulk insert attempt, to put in the sparse signatures.
	// If there are many signatures, this is potentially a high cost insert.
	// It might be better to do a select to discover the existing key IDs,
	// so we only insert new ones.
	nSigs := 0
	args = args[:0]
	for _, bhid := range blockHashesAndIDs {
		for _, sig := range proofs.BlockSignatures[string(bhid.Hash)] {
			args = append(args, bhid.ID, sig.KeyID, sig.Sig)
			nSigs++
		}
	}
	if _, err := tx.ExecContext(
		ctx,
		`INSERT OR IGNORE INTO round_`+voteType+`_signatures(block_id, key_id, signature)
VALUES (?, ?, ?)`+
			strings.Repeat(",(?,?,?) ", nSigs-1),
		args...,
	); err != nil {
		return fmt.Errorf("failed to bulk insert %s signatures: %w", voteType, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit saving %ss: %w", voteType, err)
	}

	return nil
}

func (s *Store) LoadRoundState(ctx context.Context, height uint64, round uint32) (
	phs []tmconsensus.ProposedHeader,
	prevotes, precommits tmconsensus.SparseSignatureCollection,
	err error,
) {
	defer trace.StartRegion(ctx, "LoadRoundState").End()

	// LoadRoundState is only called twice at the start of the mirror kernel,
	// so it doesn't have to be super efficient.
	tx, err := s.ro.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, prevotes, precommits, fmt.Errorf(
			"failed to begin read-only transaction: %w", err,
		)
	}
	defer tx.Rollback()

	// First, collect the validator sets belonging to proposed headers in this round.
	type dbPKH struct { // "Database public key hash".
		Hash []byte
		Keys []gcrypto.PubKey
	}
	pubKeySets := make(map[int64]dbPKH)
	rows, err := tx.QueryContext(
		ctx,
		`SELECT
vpks.hash_id, vpks.hash, vpks.idx, vpks.type, vpks.key, vpks.n_keys
FROM validator_pub_keys_for_hash AS vpks
JOIN headers ON
  (headers.validators_pub_key_hash_id = vpks.hash_id OR headers.next_validators_pub_key_hash_id = vpks.hash_id)
LEFT JOIN proposed_headers ON
  proposed_headers.header_id = headers.id
WHERE headers.height = ?1 OR (proposed_headers.height = ?1 AND proposed_headers.round = ?2)`,
		height, round,
	)
	if err != nil {
		return nil, prevotes, precommits, fmt.Errorf(
			"failed to select validator sets for proposed headers: %w", err,
		)
	}
	defer rows.Close()

	var typeName string
	var keyBytes, hash []byte
	for rows.Next() {
		var hashID int64
		var idx, nKeys int
		keyBytes = keyBytes[:0]
		hash = hash[:0]
		if err := rows.Scan(&hashID, &hash, &idx, &typeName, &keyBytes, &nKeys); err != nil {
			return nil, prevotes, precommits, fmt.Errorf(
				"failed to scan validator set public key entry: %w", err,
			)
		}

		e, ok := pubKeySets[hashID]
		if !ok {
			e = dbPKH{
				Hash: bytes.Clone(hash),
				Keys: make([]gcrypto.PubKey, nKeys),
			}
			pubKeySets[hashID] = e
		}
		e.Keys[idx], err = s.reg.Decode(typeName, bytes.Clone(keyBytes))
		if err != nil {
			return nil, prevotes, precommits, fmt.Errorf(
				"failed to decode validator key: %w", err,
			)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, prevotes, precommits, fmt.Errorf(
			"failed to iterate validator sets: %w", err,
		)
	}
	if err := rows.Close(); err != nil {
		return nil, prevotes, precommits, fmt.Errorf(
			"failed to close validator set iterator: %w", err,
		)
	}

	// Now do the same thing for the vote powers.
	type dbVPH struct {
		Hash   []byte
		Powers []uint64
	}
	powerSets := make(map[int64]dbVPH)
	rows, err = tx.QueryContext(
		ctx,
		`SELECT vps.hash_id, vps.hash, vps.idx, vps.power, vps.n_powers
FROM validator_powers_for_hash AS vps
JOIN headers ON
  (headers.validators_power_hash_id = vps.hash_id OR headers.next_validators_power_hash_id = vps.hash_id)
LEFT JOIN proposed_headers ON
  proposed_headers.header_id = headers.id
WHERE headers.height = ?1 OR (proposed_headers.height = ?1 AND proposed_headers.round = ?2)`,
		height, round,
	)
	if err != nil {
		return nil, prevotes, precommits, fmt.Errorf(
			"failed to select validator sets for proposed headers: %w", err,
		)
	}
	defer rows.Close()

	for rows.Next() {
		var hashID int64
		var power uint64
		var idx, nPowers int
		hash = hash[:0]
		if err := rows.Scan(&hashID, &hash, &idx, &power, &nPowers); err != nil {
			return nil, prevotes, precommits, fmt.Errorf(
				"failed to scan validator power set entry: %w", err,
			)
		}

		e, ok := powerSets[hashID]
		if !ok {
			e = dbVPH{
				Hash:   bytes.Clone(hash),
				Powers: make([]uint64, nPowers),
			}
			powerSets[hashID] = e
		}
		e.Powers[idx] = power
	}
	if err := rows.Err(); err != nil {
		return nil, prevotes, precommits, fmt.Errorf(
			"failed to iterate validator power sets for proposed headers: %w", err,
		)
	}
	if err := rows.Close(); err != nil {
		return nil, prevotes, precommits, fmt.Errorf(
			"failed to close validator power set iterator for proposed headers: %w", err,
		)
	}

	rows, err = tx.QueryContext(
		ctx,
		`SELECT ps.commit_proof_id, ps.block_hash, ps.key_id, ps.signature
FROM proof_signatures AS ps
JOIN headers AS hs ON hs.prev_commit_proof_id = ps.commit_proof_id
JOIN proposed_headers AS phs ON phs.header_id = hs.id
WHERE hs.height = ?1 OR (phs.height = ?1 AND phs.round = ?2)
ORDER BY ps.key_id`,
		height, round,
	)
	if err != nil {
		return nil, prevotes, precommits, fmt.Errorf(
			"failed to select previous commit proofs for proposed headers: %w", err,
		)
	}
	defer rows.Close()

	pcps := make(map[int64]map[string][]gcrypto.SparseSignature)
	var blockHash, keyID, sig []byte
	for rows.Next() {
		var cpID int64
		blockHash = blockHash[:0]
		keyID = keyID[:0]
		sig = sig[:0]
		if err := rows.Scan(&cpID, &blockHash, &keyID, &sig); err != nil {
			return nil, prevotes, precommits, fmt.Errorf(
				"failed to scan previous commit proofs for proposed headers: %w", err,
			)
		}

		m, ok := pcps[cpID]
		if !ok {
			m = make(map[string][]gcrypto.SparseSignature)
			pcps[cpID] = m
		}

		m[string(blockHash)] = append(
			m[string(blockHash)],
			gcrypto.SparseSignature{
				KeyID: bytes.Clone(keyID),
				Sig:   bytes.Clone(sig),
			},
		)
	}
	if err := rows.Err(); err != nil {
		return nil, prevotes, precommits, fmt.Errorf(
			"failed to iterate previous commit proofs for proposed headers: %w", err,
		)
	}
	if err := rows.Close(); err != nil {
		return nil, prevotes, precommits, fmt.Errorf(
			"failed to close previous commit proofs iterator for proposed headers: %w", err,
		)
	}

	// Now that we have the validator and previous commit proof details,
	// we can collect the proposed headers.
	rows, err = tx.QueryContext(
		ctx,
		`SELECT
keys.type, keys.key,
phs.user_annotations, phs.driver_annotations,
phs.signature,
hs.height, hs.hash, hs.prev_block_hash,
hs.prev_commit_proof_id,
pcp.round,
pcphash.hash,
hs.validators_pub_key_hash_id, hs.next_validators_pub_key_hash_id,
hs.validators_power_hash_id, hs.next_validators_power_hash_id,
hs.data_id,
hs.prev_app_state_hash,
hs.user_annotations, hs.driver_annotations
FROM proposed_headers AS phs
JOIN headers AS hs ON hs.id = phs.header_id
JOIN validator_pub_keys AS keys ON keys.id = phs.proposer_pub_key_id
LEFT JOIN commit_proofs AS pcp ON pcp.id = hs.prev_commit_proof_id
LEFT JOIN validator_pub_key_hashes AS pcphash ON pcphash.id = pcp.validators_pub_key_hash_id
WHERE phs.height = ?1 AND phs.round = ?2
UNION ALL
SELECT
NULL, NULL,
NULL, NULL,
NULL,
hs.height, hs.hash, hs.prev_block_hash,
hs.prev_commit_proof_id,
pcp.round,
pcphash.hash,
hs.validators_pub_key_hash_id, hs.next_validators_pub_key_hash_id,
hs.validators_power_hash_id, hs.next_validators_power_hash_id,
hs.data_id,
hs.prev_app_state_hash,
hs.user_annotations, hs.driver_annotations
FROM headers AS hs
LEFT JOIN commit_proofs AS pcp ON pcp.id = hs.prev_commit_proof_id
LEFT JOIN validator_pub_key_hashes AS pcphash ON pcphash.id = pcp.validators_pub_key_hash_id
WHERE hs.height = ?1 AND hs.id NOT IN (
  SELECT header_id FROM proposed_headers WHERE height = ?1 AND round = ?2
)
`,
		height, round,
	)
	if err != nil {
		return nil, prevotes, precommits, fmt.Errorf(
			"failed to select from proposed headers: %w", err,
		)
	}
	defer rows.Close()

	valSets := make(map[[2]int64]tmconsensus.ValidatorSet)

	var pcpValHash []byte
	for rows.Next() {
		var vkID, nvkID, vpID, nvpID int64
		var pcpID sql.NullInt64
		var pcpRound sql.Null[uint32]
		var maybeTypeName sql.NullString
		keyBytes = keyBytes[:0]
		pcpValHash = pcpValHash[:0]
		ph := tmconsensus.ProposedHeader{Round: round}
		if err := rows.Scan(
			&maybeTypeName, &keyBytes,
			&ph.Annotations.User, &ph.Annotations.Driver,
			&ph.Signature,
			&ph.Header.Height, &ph.Header.Hash, &ph.Header.PrevBlockHash,
			&pcpID,
			&pcpRound,
			&pcpValHash,
			&vkID, &nvkID,
			&vpID, &nvpID,
			&ph.Header.DataID,
			&ph.Header.PrevAppStateHash,
			&ph.Header.Annotations.User, &ph.Header.Annotations.Driver,
		); err != nil {
			return nil, prevotes, precommits, fmt.Errorf(
				"failed to scan proposed headers row: %w", err,
			)
		}
		if len(keyBytes) > 0 {
			key, err := s.reg.Decode(maybeTypeName.String, bytes.Clone(keyBytes))
			if err != nil {
				return nil, prevotes, precommits, fmt.Errorf(
					"failed to decode proposer public key from proposed headers: %w", err,
				)
			}
			ph.ProposerPubKey = key
		}

		keys := pubKeySets[vkID]
		pows := powerSets[vpID]
		if len(keys.Keys) != len(pows.Powers) {
			return nil, prevotes, precommits, fmt.Errorf(
				"validator hash mismatch: %d keys and %d powers", len(keys.Keys), len(pows.Powers),
			)
		}

		valSetKey := [2]int64{vkID, vpID}
		valSet, ok := valSets[valSetKey]
		if !ok {
			vals := make([]tmconsensus.Validator, len(keys.Keys))
			for i := range keys.Keys {
				vals[i].PubKey = keys.Keys[i]
				vals[i].Power = pows.Powers[i]
			}
			valSet = tmconsensus.ValidatorSet{
				Validators:    vals,
				PubKeyHash:    keys.Hash,
				VotePowerHash: pows.Hash,
			}
			// TODO: gassert: confirm hashes
			valSets[valSetKey] = valSet
		}
		ph.Header.ValidatorSet = valSet

		// Do it again for the next validator set.
		valSetKey = [2]int64{nvkID, nvpID}
		valSet, ok = valSets[valSetKey]
		if !ok {
			vals := make([]tmconsensus.Validator, len(keys.Keys))
			for i := range keys.Keys {
				vals[i].PubKey = keys.Keys[i]
				vals[i].Power = pows.Powers[i]
			}
			valSet = tmconsensus.ValidatorSet{
				Validators:    vals,
				PubKeyHash:    keys.Hash,
				VotePowerHash: pows.Hash,
			}
			// TODO: gassert: confirm hashes
			valSets[valSetKey] = valSet
		}
		ph.Header.NextValidatorSet = valSet

		// And assign the previous commit proofs, if applicable.
		if pcpID.Valid {
			proofs, ok := pcps[pcpID.Int64]
			if !ok {
				return nil, prevotes, precommits, fmt.Errorf(
					"QUERY BUG: missing previous commit proof (id=%d)", pcpID.Int64,
				)
			}
			if !pcpRound.Valid {
				return nil, prevotes, precommits, fmt.Errorf(
					"QUERY BUG: had previous commit proof (id=%d) but no round", pcpID.Int64,
				)
			}
			ph.Header.PrevCommitProof = tmconsensus.CommitProof{
				Round:      pcpRound.V,
				PubKeyHash: string(pcpValHash),
				Proofs:     proofs,
			}
		}

		phs = append(phs, ph)
	}
	if err := rows.Err(); err != nil {
		return nil, prevotes, precommits, fmt.Errorf(
			"failed to iterate proposed headers: %w", err,
		)
	}
	if err := rows.Close(); err != nil {
		return nil, prevotes, precommits, fmt.Errorf(
			"failed to close proposed headers iterator: %w", err,
		)
	}

	prevotes, err = s.loadRoundStateVotes(ctx, tx, height, round, "round_prevotes")
	if err != nil {
		return nil, prevotes, precommits, fmt.Errorf(
			"failed to load round state prevotes: %w", err,
		)
	}
	precommits, err = s.loadRoundStateVotes(ctx, tx, height, round, "round_precommits")
	if err != nil {
		return nil, prevotes, precommits, fmt.Errorf(
			"failed to load round state prevotes: %w", err,
		)
	}

	if len(prevotes.PubKeyHash) > 0 && len(precommits.PubKeyHash) > 0 &&
		!bytes.Equal(prevotes.PubKeyHash, precommits.PubKeyHash) {
		panic(fmt.Errorf(
			"DATABASE CONSISTENCY BUG: for height=%d and round=%d, loaded prevote pub key hash %x and precommit pub key hash %x",
			height, round, prevotes.PubKeyHash, precommits.PubKeyHash,
		))
	}

	if len(phs) == 0 && len(prevotes.BlockSignatures) == 0 && len(precommits.BlockSignatures) == 0 {
		return phs, prevotes, precommits, tmconsensus.RoundUnknownError{
			WantHeight: height, WantRound: round,
		}
	}

	return phs, prevotes, precommits, nil
}

func (s *Store) loadRoundStateVotes(
	ctx context.Context,
	tx *sql.Tx,
	height uint64,
	round uint32,
	tableName string,
) (out tmconsensus.SparseSignatureCollection, err error) {
	defer trace.StartRegion(ctx, "loadRoundStateVotes").End()

	switch tableName {
	case "round_prevotes", "round_precommits":
		// Okay.
	default:
		panic(fmt.Errorf("BUG: illegal table name %s for loadRoundStateVotes", tableName))
	}

	rows, err := tx.QueryContext(
		ctx,
		`SELECT pub_key_hash, block_hash, key_id, sig FROM `+tableName+` WHERE height=? AND round=?`,
		height, round,
	)
	if err != nil {
		return out, fmt.Errorf(
			"failed to select from %s: %w", tableName, err,
		)
	}
	defer rows.Close()

	isFirstScan := true
	var pubKeyHash, blockHash, keyID, sig []byte
	for rows.Next() {
		pubKeyHash = pubKeyHash[:0]
		blockHash = blockHash[:0]
		keyID = keyID[:0]
		sig = sig[:0]
		if err := rows.Scan(&pubKeyHash, &blockHash, &keyID, &sig); err != nil {
			return out, fmt.Errorf(
				"failed to scan %s row: %w", tableName, err,
			)
		}
		if isFirstScan {
			out.PubKeyHash = bytes.Clone(pubKeyHash)
			out.BlockSignatures = make(map[string][]gcrypto.SparseSignature)
		}
		isFirstScan = false

		out.BlockSignatures[string(blockHash)] = append(
			out.BlockSignatures[string(blockHash)],
			gcrypto.SparseSignature{
				KeyID: bytes.Clone(keyID),
				Sig:   bytes.Clone(sig),
			},
		)
	}
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf(
			"error when iterating %s: %w", tableName, err,
		)
	}
	if err := rows.Close(); err != nil {
		return out, fmt.Errorf(
			"error when closing %s iterator: %w", tableName, err,
		)
	}

	return out, nil
}

func pragmasRW(ctx context.Context, db *sql.DB) error {
	defer trace.StartRegion(ctx, "pragmasRW").End()

	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = ON;`); err != nil {
		return fmt.Errorf("failed to set foreign keys on: %w", err)
	}

	// https://www.sqlite.org/lang_analyze.html#periodically_run_pragma_optimize_
	// "Applications that use long-lived database connections should run `PRAGMA optimize=0x10002;`
	// when the connection is first opened,
	// and then also run `PRAGMA optimize;` periodically,
	// perhaps once per day, or more if the database is evolving rapidly."
	//
	// TODO: according to https://www.sqlite.org/lang_analyze.html#automatically_running_analyze
	// we should probably set up a timer to periodically re-run PRAGMA optimize,
	// as it is influenced by exactly which queries are run,
	// and those statistics are held in-memory.
	if _, err := db.ExecContext(ctx, `PRAGMA optimize(0x10002);`); err != nil {
		return fmt.Errorf("failed to run startup PRAGMA optimize: %w", err)
	}

	return nil
}

func pragmasRO(ctx context.Context, db *sql.DB) error {
	defer trace.StartRegion(ctx, "pragmasRO").End()

	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = ON;`); err != nil {
		return fmt.Errorf("failed to set foreign keys on: %w", err)
	}

	// Skip PRAGMA optimize for the read-only pragmas.

	return nil
}
