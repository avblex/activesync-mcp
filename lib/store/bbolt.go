// Package store provides durable per-account state persistence for the
// activesync-mcp server. The persistence layer is bbolt; per-account
// views satisfy the eas.StateStore interface so the EAS client can
// remain storage-agnostic (and library-extractable).
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hstern/go-activesync/eas"

	"go.etcd.io/bbolt"
	bberrors "go.etcd.io/bbolt/errors"
)

// Bucket layout
//
//	policykey   -> { account => key }
//	synckey     -> { account => bucket of { folderID => key } }
//	folders     -> { account => bucket of { folderID => json(eas.Folder) } }
//
// The folders bucket caches the cumulative folder hierarchy each
// account has seen via FolderSync. It exists because the EAS
// FolderSync command returns *deltas* since the persisted SyncKey;
// MCP tools that want "the current folder list" need a place to
// accumulate those deltas across calls. See FolderCache.Apply.
const (
	bucketPolicyKey = "policykey"
	bucketSyncKey   = "synckey"
	bucketFolders   = "folders"
)

// ErrLocked means another process currently owns the selected state file.
// Callers may use errors.Is to distinguish contention from I/O or corruption.
var ErrLocked = errors.New("state database is locked")

const poolLockTimeout = 100 * time.Millisecond

// DB wraps a single bbolt database serving all configured accounts.
type DB struct {
	db   *bbolt.DB
	slot int
}

// Open creates or opens the bbolt file at path, creating the parent
// directory if needed. Buckets are created on first open.
func Open(path string) (*DB, error) {
	return open(path, 1, 5*time.Second)
}

// OpenPool opens the first available file in a bounded, stable pool. Slot one
// uses basePath; later slots insert "-N" before its extension. Stable names
// preserve each process's EAS SyncKey and device identity across restarts.
func OpenPool(basePath string, maxSlots int) (*DB, error) {
	if maxSlots < 1 {
		return nil, fmt.Errorf("store: open pool: max slots must be positive")
	}
	for slot := 1; slot <= maxSlots; slot++ {
		db, err := open(poolPath(basePath, slot), slot, poolLockTimeout)
		if err == nil {
			return db, nil
		}
		if !errors.Is(err, ErrLocked) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("store: all %d state database slots are in use: %w", maxSlots, ErrLocked)
}

func poolPath(basePath string, slot int) string {
	if slot == 1 {
		return basePath
	}
	ext := filepath.Ext(basePath)
	stem := strings.TrimSuffix(basePath, ext)
	return fmt.Sprintf("%s-%d%s", stem, slot, ext)
}

func open(path string, slot int, timeout time.Duration) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("store: create state dir: %w", err)
	}
	bdb, err := bbolt.Open(path, 0o600, &bbolt.Options{Timeout: timeout})
	if err != nil {
		// bbolt holds an exclusive flock on the database file, so a
		// second activesync-mcp talking to the same state.db will see
		// ErrTimeout after the caller's open budget. Surface the actual
		// cause instead of the cryptic "timeout".
		if errors.Is(err, bberrors.ErrTimeout) {
			return nil, fmt.Errorf("store: open %s: another activesync-mcp process appears to be running (state database is locked): %w", path, ErrLocked)
		}
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	if err := bdb.Update(func(tx *bbolt.Tx) error {
		for _, name := range []string{bucketPolicyKey, bucketSyncKey, bucketFolders} {
			if _, err := tx.CreateBucketIfNotExists([]byte(name)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		_ = bdb.Close()
		return nil, fmt.Errorf("store: create buckets: %w", err)
	}
	return &DB{db: bdb, slot: slot}, nil
}

// Slot returns the one-based stable pool slot owned by this handle.
func (d *DB) Slot() int { return d.slot }

// Close releases the file lock and flushes any in-flight writes.
func (d *DB) Close() error { return d.db.Close() }

// AccountState returns an eas.StateStore scoped to the given account name.
func (d *DB) AccountState(account string) eas.StateStore {
	return &accountState{db: d.db, account: []byte(account)}
}

// accountState satisfies eas.StateStore by indexing into the shared bbolt
// buckets with the account name.
type accountState struct {
	db      *bbolt.DB
	account []byte
}

func (a *accountState) PolicyKey(_ context.Context) (string, error) {
	var out string
	err := a.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketPolicyKey))
		if b == nil {
			return nil
		}
		if v := b.Get(a.account); v != nil {
			out = string(v)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("store: PolicyKey: %w", err)
	}
	return out, nil
}

func (a *accountState) SetPolicyKey(_ context.Context, key string) error {
	if err := a.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketPolicyKey))
		if key == "" {
			return b.Delete(a.account)
		}
		return b.Put(a.account, []byte(key))
	}); err != nil {
		return fmt.Errorf("store: SetPolicyKey: %w", err)
	}
	return nil
}

func (a *accountState) SyncKey(_ context.Context, folderID string) (string, error) {
	out := "0"
	err := a.db.View(func(tx *bbolt.Tx) error {
		root := tx.Bucket([]byte(bucketSyncKey))
		if root == nil {
			return nil
		}
		acct := root.Bucket(a.account)
		if acct == nil {
			return nil
		}
		if v := acct.Get([]byte(folderID)); v != nil {
			out = string(v)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("store: SyncKey: %w", err)
	}
	return out, nil
}

func (a *accountState) SetSyncKey(_ context.Context, folderID, key string) error {
	if err := a.db.Update(func(tx *bbolt.Tx) error {
		root := tx.Bucket([]byte(bucketSyncKey))
		acct, err := root.CreateBucketIfNotExists(a.account)
		if err != nil {
			return err
		}
		if key == "" || key == "0" {
			return acct.Delete([]byte(folderID))
		}
		return acct.Put([]byte(folderID), []byte(key))
	}); err != nil {
		return fmt.Errorf("store: SetSyncKey: %w", err)
	}
	return nil
}

// ResetAccount drops every persisted key for the given account. Used by
// the doctor / debug paths if a server-side state is corrupt and a full
// resync is the cleanest recovery.
func (d *DB) ResetAccount(account string) error {
	return d.db.Update(func(tx *bbolt.Tx) error {
		if b := tx.Bucket([]byte(bucketPolicyKey)); b != nil {
			if err := b.Delete([]byte(account)); err != nil {
				return err
			}
		}
		for _, name := range []string{bucketSyncKey, bucketFolders} {
			if b := tx.Bucket([]byte(name)); b != nil {
				if sub := b.Bucket([]byte(account)); sub != nil {
					if err := b.DeleteBucket([]byte(account)); err != nil {
						return err
					}
					_ = sub
				}
			}
		}
		return nil
	})
}

// FolderCache returns the per-account folder-list cache scoped to the
// given account name. The cache is the durable mirror of every
// FolderSync delta this account has applied.
func (d *DB) FolderCache(account string) *FolderCache {
	return &FolderCache{db: d.db, account: []byte(account)}
}

// FolderCache is the per-account view of cached folder records.
// See store.bbolt's package doc for the bucket layout.
type FolderCache struct {
	db      *bbolt.DB
	account []byte
}

// All returns every cached folder for the account, in arbitrary order.
// On a fresh account (or after ResetAccount) this is empty until the
// first Apply.
func (f *FolderCache) All() ([]eas.Folder, error) {
	var out []eas.Folder
	err := f.db.View(func(tx *bbolt.Tx) error {
		root := tx.Bucket([]byte(bucketFolders))
		if root == nil {
			return nil
		}
		acct := root.Bucket(f.account)
		if acct == nil {
			return nil
		}
		return acct.ForEach(func(_, v []byte) error {
			var fld eas.Folder
			if err := json.Unmarshal(v, &fld); err != nil {
				return fmt.Errorf("folder cache: decode: %w", err)
			}
			out = append(out, fld)
			return nil
		})
	})
	if err != nil {
		return nil, fmt.Errorf("store: FolderCache.All: %w", err)
	}
	return out, nil
}

// Apply folds a FolderSync delta into the cache: Added and Updated
// folders are inserted/replaced; Deleted server IDs are removed. A
// fresh-start sync (the server returned the entire hierarchy in
// Added because the persisted SyncKey was "0") is just a sequence of
// inserts — no special-casing needed.
func (f *FolderCache) Apply(fs *eas.FolderSyncResult) error {
	if fs == nil {
		return nil
	}
	return f.db.Update(func(tx *bbolt.Tx) error {
		root := tx.Bucket([]byte(bucketFolders))
		if root == nil {
			return errors.New("folder cache: missing root bucket")
		}
		acct, err := root.CreateBucketIfNotExists(f.account)
		if err != nil {
			return err
		}
		for _, fld := range fs.Added {
			if err := putFolder(acct, fld); err != nil {
				return err
			}
		}
		for _, fld := range fs.Updated {
			if err := putFolder(acct, fld); err != nil {
				return err
			}
		}
		for _, id := range fs.Deleted {
			if err := acct.Delete([]byte(id)); err != nil {
				return err
			}
		}
		return nil
	})
}

func putFolder(b *bbolt.Bucket, fld eas.Folder) error {
	body, err := json.Marshal(fld)
	if err != nil {
		return fmt.Errorf("folder cache: encode: %w", err)
	}
	return b.Put([]byte(fld.ServerID), body)
}

// Clear empties the per-account folder cache. Called in the recovery
// path when FolderSync returns status 9 (server-side state corrupt):
// after we reset the SyncKey to "0" and the server replays the full
// hierarchy in Added, the cache must start fresh — otherwise stale
// entries from before the corruption would survive the recovery.
func (f *FolderCache) Clear() error {
	if err := f.db.Update(func(tx *bbolt.Tx) error {
		root := tx.Bucket([]byte(bucketFolders))
		if root == nil {
			return nil
		}
		if root.Bucket(f.account) == nil {
			return nil
		}
		return root.DeleteBucket(f.account)
	}); err != nil {
		return fmt.Errorf("store: FolderCache.Clear: %w", err)
	}
	return nil
}
