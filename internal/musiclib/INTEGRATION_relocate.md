# Relocate - UI integration flow

## Goal

Let the user find and re-link tracks that have moved on disk **without ever touching
the original `collection.nml`** or any media file.

---

## Step 1 - Scan (read-only)

Trigger: user opens the Library tab → "Check for missing files" button.

```
present, missing := ScanMissing(library.Tracks)
```

Display: "N files missing, M present." If N = 0 → done, show toast.
No write access required at this step.

---

## Step 2 - Pick search roots

Show a folder-picker dialog (Fyne `dialog.ShowFolderOpen` or a custom
`SmartSelect`-equivalent multi-root picker). Pre-populate with likely roots:
- Parent dir of `collection.nml`
- OS Music folder (`os.UserHomeDir() + "/Music"` or equivalent)
- Any previously-used roots (persisted in app config)

User confirms → call `BuildIndex(selectedRoots)`. Show progress indicator
(WalkDir can be slow on large drives; run in a goroutine, update a label via
`fyne.Do`).

---

## Step 3 - Relocate (read-only)

```
candidates := Relocate(missing, index)
```

Display a table:
| Track | Old path | New path | Confidence |
|-------|----------|----------|-----------|
| Lost Track | C:\old\… | D:\new\… | 100% |
| Ambiguous  | C:\old\… | D:\new\… |  90% |

- Confidence 1.0 → pre-checked "Apply"
- Confidence 0.9 → pre-checked with a note "size match"
- Confidence 0.5 → unchecked, requires explicit user opt-in

User may uncheck any row or manually pick a different file
(file-picker per row for the 0.5 cases).

---

## Step 4 - Confirm + BACKUP FIRST

Before writing anything:

1. **Call the backup feature** (see `internal/musiclib` backup implementation, TBD).
   The backup should copy `collection.nml` to
   `collection.nml.bak-YYYYMMDD-HHMMSS` **in the same directory** before proceeding.
   Block the fix step until the backup reports success.

2. Show a confirmation dialog:
   > "Apply N path fixes? A backup has been saved to collection.nml.bak-…
   >  Your original file will NOT be overwritten - a new file will be created."

Only proceed after explicit user confirmation.

---

## Step 5 - WriteFixedCollection to a NEW file

```go
plan := FixPlan{Fixes: approvedCandidates}

// New file: collection.fixed.nml next to the original, or user-chosen path.
dstPath := strings.TrimSuffix(collectionPath, ".nml") + ".fixed.nml"
// Let user override via SaveFilePicker.

src, _ := os.Open(collectionPath)          // original, read-only
dst, _ := os.Create(dstPath)               // NEW file, caller-owned
fixed, err := WriteFixedCollection(src, plan, dst)
src.Close(); dst.Close()
```

- `collectionPath` (the original) is **never passed as dst** - callers must not
  pass the same path to both `os.Open` and `os.Create`.
- Display result: "Fixed N tracks. Saved to collection.fixed.nml."
- Offer a "Set as active collection" button that updates the app config to point
  Traktor import at the new file (the user still needs to tell Traktor Pro itself
  to load the new file - document this in the tooltip).

---

## Safety invariants (enforced in code, not just UI)

| Invariant | Where enforced |
|-----------|----------------|
| No media file is read or written | `ScanMissing` / `BuildIndex` use `os.Stat` / `WalkDir` only |
| Original NML never opened for write | `WriteFixedCollection` takes `src io.Reader`, `dst io.Writer` - callers own the file handles |
| No network calls | Entire flow is local stdlib only |
| Backup before fix | UI flow gate (Step 4) - cannot be skipped by the UI |

---

## Error handling

- `BuildIndex` root unreadable → skip silently, log to logbus.
- `WriteFixedCollection` XML decode error → return partial count + error; UI shows
  "Failed after N fixes: …" and keeps the partial output file for inspection.
- Backup failure → abort the fix step, surface error prominently.

---

## Future

- NML token-rewrite (`WriteFixedCollection`) is already implemented. A later pass
  could offer "apply directly to original after backup confirmation" using `os.Rename`
  (atomic on same FS) after a successful write to a temp file.
- Multi-root parallel WalkDir for faster indexing on large libraries.
- Confidence scoring could incorporate BPM / title fuzzy match for 0.5 cases.
