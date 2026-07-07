# Backup integration

## backupRoot location

```go
root, err := config.DataPath("library-backups")
```

Resolves to `<OS config dir>/rave-mate/library-backups/` (created on first use).

## Mandatory pre-mutation call

Before any feature that rewrites a collection (path-fix, tag-write, re-encode, etc.):

```go
bk, err := musiclib.BackupCollection(install, backupRoot)
if err != nil {
    return fmt.Errorf("backup failed, aborting mutation: %w", err)
}
log.Printf("backup at %s (%d bytes)", bk.Path, bk.SizeBytes)
// proceed with mutation ...
```

Never mutate if the backup errors.

## "Backup now" action

Wire to a button/tray-menu item in the Library tab:

```go
bk, err := musiclib.BackupCollection(install, backupRoot)
// show success toast or error dialog
```

## "Manage backups" list

```go
backups, err := musiclib.ListBackups(backupRoot)
// render: bk.When (formatted), bk.SizeBytes, bk.Path
```

Sorted newest-first by ListBackups.

## Prune (retention setting)

Default: keep 10. User-configurable via a `LibraryBackupKeep int` field on
`config.Features` (add when wiring settings UI):

```go
if err := musiclib.PruneBackups(backupRoot, cfg.Features.Library.BackupKeep); err != nil {
    log.Println("prune:", err)
}
```

Call after each successful BackupCollection so the dir stays bounded.

## Safety invariants

- `BackupCollection` and `PruneBackups` operate only under backupRoot.
- `PruneBackups` rejects an empty root and any path containing "Native Instruments".
- Source files are opened read-only (`os.Open`); destinations are created fresh.
- `io.Copy` streams the collection - no whole-file load; safe for the ~266 MB NML.
