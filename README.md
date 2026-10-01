# CellEdit

A terminal CSV editor with vim-style keybindings.

```
ce <file.csv>
```

If the file does not exist it is created on first save.

A sample file (`sample.csv`, 200 rows of coffee shop sales data) is included in the repo for trying things out:

```
ce sample.csv
```

<img width="720" height="566" alt="Image" src="https://github.com/user-attachments/assets/0cc9ac44-a819-4a2f-85a7-8fdf35f3a2c0" />

---

## Build

Requires Go 1.21+.

```
git clone https://github.com/huyng/celledit.git
go build -o ce .
```

## Install

Or use the install script to build and install to `~/.local/bin/`:

```
git clone https://github.com/huyng/celledit.git
./install.sh
```

---

## Modes

| Mode | Description |
|------|-------------|
| **NORMAL** | Navigate and run commands |
| **INSERT** | Edit the current cell |
| **COMMAND** | Type a `:` command |

---

## Normal mode

### Navigation

| Key | Action |
|-----|--------|
| `h` / `←` | Move left |
| `l` / `→` | Move right |
| `j` / `↓` | Move down |
| `k` / `↑` | Move up |
| `w` | Next column |
| `b` | Previous column |
| `0` or `^` | First column |
| `$` | Last column |
| `gg` | First row |
| `G` | Last row |
| `Tab` | Move right |
| `Ctrl+F` | Page down |
| `Ctrl+B` | Page up |
| `Ctrl+D` | Half-page down |
| `Ctrl+U` | Half-page up |

### Editing

| Key | Action |
|-----|--------|
| `i` | Enter insert mode (cursor at start) |
| `a` / `A` | Enter insert mode (cursor at end) |
| `Enter` | Enter insert mode (cursor at start) |
| `x` | Clear current cell |

### Row operations

| Key | Action |
|-----|--------|
| `dd` | Delete row (cut into register) |
| `yy` | Yank (copy) row |
| `ra` | Insert blank row below |
| `ri` | Insert blank row above |
| `o` | Insert blank row below, enter insert mode |
| `O` | Insert blank row above, enter insert mode |
| `p` | Paste row below |
| `P` | Paste row above |

### Column operations

| Key | Action |
|-----|--------|
| `dc` | Delete column (cut into register) |
| `yc` | Yank (copy) column |
| `ca` | Insert blank column to the right |
| `ci` | Insert blank column to the left |
| `p` | Paste column to the right |
| `P` | Paste column to the left |

> `p` and `P` paste a row or column depending on what was last yanked.

### Sorting

| Key | Action |
|-----|--------|
| `s` | Toggle sort ascending/descending by current column |
| `sj` | Sort current column descending |
| `sk` | Sort current column ascending |

The sorted column's header shows `▲` or `▼`. Sorts are undoable with `u`. When header mode is active, row 1 is excluded from sorting.

### Undo / redo

| Key | Action |
|-----|--------|
| `u` | Undo |
| `Ctrl+R` | Redo |


| Action | Effect |
|--------|--------|
| Single click | Navigate to cell |
| Double click | Navigate to cell and enter insert mode |

### Other

| Key | Action |
|-----|--------|
| `Ctrl+L` | Redraw screen |
| `Ctrl+Q` | Quit (warns if unsaved) |
| `:` | Enter command mode |

---

## Insert mode

The cell scrolls horizontally if content exceeds the column width. Confirm with `Enter` (moves down), `Tab` (moves right), `Esc`, or `Ctrl+C`. All four accept the edit.

| Key | Action |
|-----|--------|
| `Ctrl+F` / `→` | Forward one character |
| `Ctrl+B` / `←` | Back one character |
| `Alt+F` | Forward one word |
| `Alt+B` | Back one word |
| `Ctrl+A` / `Home` | Beginning of cell |
| `Ctrl+E` / `End` | End of cell |
| `Ctrl+W` | Delete word backward |
| `Ctrl+K` | Delete to end of cell |
| `Ctrl+D` / `Delete` | Delete character forward |
| `Ctrl+U` | Delete to beginning of cell |
| `Backspace` | Delete character backward |
| `Esc` / `Ctrl+C` | Accept edit, return to normal mode |
| `Enter` | Accept edit, move down |
| `Tab` | Accept edit, move right |

---

## Command mode

Enter with `:` from normal mode. `Esc` or `Ctrl+C` cancels.

| Command | Action |
|---------|--------|
| `:w` | Save |
| `:q` | Quit (warns if unsaved) |
| `:q!` | Force quit without saving |
| `:wq` | Save and quit |
| `:N` | Jump to row N |
| `:set header` | Treat row 1 as a frozen header (excluded from sorts) |
| `:set noheader` | Disable header mode |

---

