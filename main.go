package main

import (
	"encoding/csv"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
)

const (
	maxColWidth = 30
	minColWidth = 4
	// Width of the row-number gutter (e.g. "  123 │")
	rowNumWidth = 6
)

// Mode is the current editor mode (vim-style).
type Mode int

const (
	ModeNormal    Mode = iota
	ModeInsert         // editing a cell
	ModeCommand        // typing a ':' command
	ModeVisual         // visual line selection (Shift+V)
	ModeVisualCol      // visual column selection (v)
)

// snapshot captures the full grid state for undo/redo.
type snapshot struct {
	data   [][]string
	curRow int
	curCol int
}

// Editor holds all state for the CSV editor.
type Editor struct {
	screen   tcell.Screen
	data     [][]string
	filename string
	modified bool

	curRow int // cursor row (0-indexed into data)
	curCol int // cursor column (0-indexed)

	// Top-left corner of the visible viewport.
	viewRow int
	viewCol int

	colWidths []int // computed display width per column

	mode Mode

	// Insert mode: buffer for the cell being edited.
	editBuf    string
	editCur    int    // rune cursor position within editBuf
	editOffset int    // first visible rune index (horizontal scroll within cell)
	origCell   string // value before entering insert mode (for Esc cancel)

	// Command mode: text after ':'.
	cmdBuf string

	// Status message shown at the bottom right.
	status string

	// Pending multi-key prefixes.
	gPressed bool // 'g' was pressed, waiting for second 'g'
	dPressed bool // 'd' was pressed, waiting for second 'd'
	yPressed bool // 'y' was pressed, waiting for second 'y'
	sPressed bool // 's' was pressed, waiting for optional 'j'/'k'
	cPressed bool // 'c' was pressed, waiting for 'a'/'i' (column insert)
	rPressed bool // 'r' was pressed, waiting for 'a'/'i' (row insert)

	// Yank register for columns (yankIsCol=true) or rows (yankIsCol=false).
	yankCols  [][]string // one or more yanked columns (when yankIsCol)
	yankRows  [][]string // one or more yanked rows
	yankIsCol bool

	// Visual mode anchors.
	anchorRow int // anchor for visual line mode (V)
	anchorCol int // anchor for visual column mode (v)

	// Undo/redo stacks (snapshot-based).
	undoStack []snapshot
	redoStack []snapshot

	// Mouse: track last click for double-click detection.
	lastClickTime time.Time
	lastClickRow  int
	lastClickCol  int

	// headerRow: when true, row 0 is treated as a frozen header and excluded from sorts.
	headerRow bool

	// sortCol is the column last sorted (-1 = none), sortDesc is the direction.
	sortCol  int
	sortDesc bool
}

// colName converts a 0-indexed column number to Excel-style name (A, B, …, Z, AA, …).
func colName(n int) string {
	s := ""
	for n >= 0 {
		s = string(rune('A'+n%26)) + s
		n = n/26 - 1
	}
	return s
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: celledit <file.csv>")
		os.Exit(1)
	}

	filename := os.Args[1]
	data, err := readCSV(filename)
	if err != nil {
		if os.IsNotExist(err) {
			// New file: start with a blank 3×3 grid.
			data = [][]string{{"", "", ""}, {"", "", ""}, {"", "", ""}}
		} else {
			fmt.Fprintf(os.Stderr, "celledit: %v\n", err)
			os.Exit(1)
		}
	}
	if len(data) == 0 {
		data = [][]string{{"", "", ""}}
	}

	screen, err := tcell.NewScreen()
	if err != nil {
		fmt.Fprintf(os.Stderr, "celledit: %v\n", err)
		os.Exit(1)
	}
	if err := screen.Init(); err != nil {
		fmt.Fprintf(os.Stderr, "celledit: %v\n", err)
		os.Exit(1)
	}
	defer screen.Fini()

	screen.SetStyle(tcell.StyleDefault)
	screen.EnableMouse()
	screen.Clear()

	ed := &Editor{
		screen:    screen,
		data:      data,
		filename:  filename,
		sortCol:   -1,
		headerRow: detectHeader(data),
	}
	ed.computeColWidths()
	ed.run()
}

// ── CSV I/O ──────────────────────────────────────────────────────────────────

func readCSV(filename string) ([][]string, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1 // allow ragged rows
	return r.ReadAll()
}

func writeCSV(filename string, data [][]string) error {
	f, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	return w.WriteAll(data)
}

// ── Header detection ─────────────────────────────────────────────────────────

func isNumeric(s string) bool {
	_, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return err == nil && strings.TrimSpace(s) != ""
}

// detectHeader returns true if the first row looks like a header.
// Heuristic: row 0 contains no numeric cells, and more than half of
// the remaining rows contain at least one numeric cell.
func detectHeader(data [][]string) bool {
	if len(data) < 2 {
		return false
	}
	// Any numeric value in row 0 → probably not a header.
	for _, cell := range data[0] {
		if isNumeric(cell) {
			return false
		}
	}
	// Count data rows that contain at least one numeric cell.
	numericRows := 0
	for _, row := range data[1:] {
		for _, cell := range row {
			if isNumeric(cell) {
				numericRows++
				break
			}
		}
	}
	return numericRows > len(data[1:])/2
}

// ── Data helpers ─────────────────────────────────────────────────────────────

func (ed *Editor) numRows() int { return len(ed.data) }
func (ed *Editor) numCols() int { return len(ed.colWidths) }

func (ed *Editor) getCell(r, c int) string {
	if r < 0 || r >= len(ed.data) || c < 0 || c >= len(ed.data[r]) {
		return ""
	}
	return ed.data[r][c]
}

func (ed *Editor) setCell(r, c int, val string) {
	// Grow rows and columns as needed.
	for len(ed.data) <= r {
		ed.data = append(ed.data, []string{})
	}
	for len(ed.data[r]) <= c {
		ed.data[r] = append(ed.data[r], "")
	}
	for len(ed.colWidths) <= c {
		ed.colWidths = append(ed.colWidths, minColWidth)
	}
	ed.data[r][c] = val
	ed.modified = true
	// Widen the column if needed (but never beyond maxColWidth).
	w := len([]rune(val))
	if w < minColWidth {
		w = minColWidth
	}
	if w > maxColWidth {
		w = maxColWidth
	}
	if w > ed.colWidths[c] {
		ed.colWidths[c] = w
	}
}

// computeColWidths recalculates column widths from scratch based on cell content.
func (ed *Editor) computeColWidths() {
	maxCols := 0
	for _, row := range ed.data {
		if len(row) > maxCols {
			maxCols = len(row)
		}
	}
	ed.colWidths = make([]int, maxCols)
	for i := range ed.colWidths {
		ed.colWidths[i] = minColWidth
	}
	for _, row := range ed.data {
		for j, cell := range row {
			w := len([]rune(cell))
			if w > ed.colWidths[j] {
				ed.colWidths[j] = w
			}
			if ed.colWidths[j] > maxColWidth {
				ed.colWidths[j] = maxColWidth
			}
		}
	}
}

// clampCursor ensures curRow/curCol stay within valid bounds.
func (ed *Editor) clampCursor() {
	nr, nc := ed.numRows(), ed.numCols()
	if nr == 0 {
		ed.data = append(ed.data, []string{})
		nr = 1
	}
	if nc == 0 {
		ed.colWidths = append(ed.colWidths, minColWidth)
		nc = 1
	}
	if ed.curRow < 0 {
		ed.curRow = 0
	}
	if ed.curRow >= nr {
		ed.curRow = nr - 1
	}
	if ed.curCol < 0 {
		ed.curCol = 0
	}
	if ed.curCol >= nc {
		ed.curCol = nc - 1
	}
}

// ── Viewport scrolling ────────────────────────────────────────────────────────

// scrollToCursor adjusts viewRow/viewCol so the cursor cell is always visible.
func (ed *Editor) scrollToCursor(sw, sh int) {
	// Vertical scroll.
	if ed.headerRow {
		// Row 0 is frozen and always visible; scroll the body (rows 1+).
		dataH := sh - 4 // col-hdr + col-border + frozen-hdr + hdr-sep + status
		if dataH < 1 {
			dataH = 1
		}
		if ed.curRow != 0 {
			if ed.curRow < ed.viewRow {
				ed.viewRow = ed.curRow
			} else if ed.curRow >= ed.viewRow+dataH {
				ed.viewRow = ed.curRow - dataH + 1
			}
		}
		// Body viewport must start at row 1 or later (row 0 is the frozen header).
		if ed.viewRow < 1 {
			ed.viewRow = 1
		}
	} else {
		dataH := sh - 3 // col-hdr + col-border + status
		if dataH < 1 {
			dataH = 1
		}
		if ed.curRow < ed.viewRow {
			ed.viewRow = ed.curRow
		} else if ed.curRow >= ed.viewRow+dataH {
			ed.viewRow = ed.curRow - dataH + 1
		}
		if ed.viewRow < 0 {
			ed.viewRow = 0
		}
	}

	// Horizontal scroll: keep curCol visible.
	if ed.curCol < ed.viewCol {
		ed.viewCol = ed.curCol
	}
	availW := sw - rowNumWidth - 1 // space available for data columns
	// Advance viewCol until curCol fits on screen.
	for ed.viewCol < ed.curCol {
		needed := 0
		for c := ed.viewCol; c <= ed.curCol; c++ {
			needed += ed.colWidths[c]
			if c < ed.curCol {
				needed++ // column separator
			}
		}
		if needed <= availW {
			break
		}
		ed.viewCol++
	}
}

// ── Main loop ─────────────────────────────────────────────────────────────────

func (ed *Editor) run() {
	for {
		sw, sh := ed.screen.Size()
		ed.scrollToCursor(sw, sh)
		ed.draw(sw, sh)

		ev := ed.screen.PollEvent()
		switch ev := ev.(type) {
		case *tcell.EventResize:
			ed.screen.Sync()
		case *tcell.EventKey:
			if ed.handleKey(ev) {
				return // quit
			}
		case *tcell.EventMouse:
			ed.handleMouse(ev, sw, sh)
		}
	}
}

// ── Drawing ───────────────────────────────────────────────────────────────────

// putChar writes a single rune to the screen at (x, y).
func (ed *Editor) putChar(x, y int, r rune, style tcell.Style) {
	ed.screen.SetContent(x, y, r, nil, style)
}

// drawText writes text left-aligned into a field of exactly maxW cells,
// padding with spaces or truncating with '>' as needed.
func (ed *Editor) drawText(x, y, maxW int, style tcell.Style, text string) {
	runes := []rune(text)
	for i := 0; i < maxW; i++ {
		r := ' '
		if i < len(runes) {
			r = runes[i]
		}
		ed.screen.SetContent(x+i, y, r, nil, style)
	}
}

func (ed *Editor) draw(sw, sh int) {
	ed.screen.Clear()

	// ── Colour palette ──
	styleDefault := tcell.StyleDefault
	styleHeader := tcell.StyleDefault.Bold(true).Foreground(tcell.ColorAqua)
	styleCurHdr := tcell.StyleDefault.Bold(true).Background(tcell.ColorTeal).Foreground(tcell.ColorWhite)
	styleCurCell := tcell.StyleDefault.Background(tcell.ColorYellow).Foreground(tcell.ColorBlack).Bold(true)
	styleInsert := tcell.StyleDefault.Background(tcell.ColorGreen).Foreground(tcell.ColorBlack)
	styleRowNum := tcell.StyleDefault.Foreground(tcell.ColorGray)
	styleSep := tcell.StyleDefault.Foreground(tcell.ColorGray)
	styleStatus := tcell.StyleDefault.Dim(true)
	styleCmd := tcell.StyleDefault.Background(tcell.ColorBlack).Foreground(tcell.ColorWhite)
	// styleHdrRow is used for the frozen header row when :set header is on.
	styleHdrRow := tcell.StyleDefault.Bold(true).Foreground(tcell.ColorAqua)
	// styleVisual highlights rows/columns in visual selections.
	styleVisual := tcell.StyleDefault.Background(tcell.ColorNavy).Foreground(tcell.ColorWhite)
	styleVisualCol := tcell.StyleDefault.Background(tcell.ColorPurple).Foreground(tcell.ColorWhite)

	// ── Column header row (y=0) ──
	// Top-left corner: the row-number gutter.
	ed.drawText(0, 0, rowNumWidth, styleHeader, strings.Repeat(" ", rowNumWidth))
	ed.putChar(rowNumWidth, 0, '│', styleHeader)

	x := rowNumWidth + 1
	for c := ed.viewCol; c < ed.numCols() && x < sw; c++ {
		w := ed.colWidths[c]
		if x+w > sw {
			w = sw - x
		}
		hdrStyle := styleHeader
		if c == ed.curCol {
			hdrStyle = styleCurHdr
		}
		label := colName(c)
		if c == ed.sortCol {
			if ed.sortDesc {
				label += " ▼"
			} else {
				label += " ▲"
			}
		}
		ed.drawText(x, 0, w, hdrStyle, centerPad(label, w))
		x += w
		if x < sw {
			ed.putChar(x, 0, '│', styleHeader)
			x++
		}
	}

	// ── Header border (y=1) ──
	ed.drawText(0, 1, rowNumWidth, styleHeader, strings.Repeat("─", rowNumWidth))
	ed.putChar(rowNumWidth, 1, '┼', styleHeader)
	x = rowNumWidth + 1
	for c := ed.viewCol; c < ed.numCols() && x < sw; c++ {
		w := ed.colWidths[c]
		if x+w > sw {
			w = sw - x
		}
		ed.drawText(x, 1, w, styleHeader, strings.Repeat("─", w))
		x += w
		if x < sw {
			ed.putChar(x, 1, '┼', styleHeader)
			x++
		}
	}

	// ── Data rows ──
	// drawRow renders one data row at screen y position.
	drawRow := func(dataRow, y int) {
		if dataRow < ed.numRows() {
			numStr := fmt.Sprintf("%*d", rowNumWidth-1, dataRow+1)
			// Highlight the row number itself on the current row.
			numStyle := styleRowNum
			if dataRow == ed.curRow {
				numStyle = styleCurHdr
			}
			ed.drawText(0, y, rowNumWidth-1, numStyle, numStr)
			ed.putChar(rowNumWidth-1, y, ' ', numStyle)
		} else {
			// Past end of data: blank gutter with a '~' like vim.
			ed.putChar(0, y, '~', styleRowNum)
			ed.drawText(1, y, rowNumWidth-1, styleDefault, strings.Repeat(" ", rowNumWidth-1))
		}

		// Gutter separator; use header row style for the frozen header, plain otherwise.
		gutterStyle := styleSep
		if ed.headerRow && dataRow == 0 {
			gutterStyle = styleHdrRow
		}
		ed.putChar(rowNumWidth, y, '│', gutterStyle)

		// Cells.
		cx := rowNumWidth + 1
		for c := ed.viewCol; c < ed.numCols() && cx < sw; c++ {
			w := ed.colWidths[c]
			if cx+w > sw {
				w = sw - cx
			}
			isCur := (dataRow == ed.curRow && c == ed.curCol)

			if dataRow < ed.numRows() {
				var cellStyle tcell.Style
				var content string

				visLo, visHi := 0, -1
				if ed.mode == ModeVisual {
					visLo, visHi = ed.selectionRange()
				}
				colLo, colHi := 0, -1
				if ed.mode == ModeVisualCol {
					colLo, colHi = ed.selectionColRange()
				}
				switch {
				case isCur && ed.mode == ModeInsert:
					cellStyle = styleInsert
					content = ed.editBuf
				case isCur:
					cellStyle = styleCurCell
					content = ed.getCell(dataRow, c)
				case ed.mode == ModeVisual && dataRow >= visLo && dataRow <= visHi:
					cellStyle = styleVisual
					content = ed.getCell(dataRow, c)
				case ed.mode == ModeVisualCol && c >= colLo && c <= colHi:
					cellStyle = styleVisualCol
					content = ed.getCell(dataRow, c)
				case ed.headerRow && dataRow == 0:
					cellStyle = styleHdrRow
					content = ed.getCell(dataRow, c)
				default:
					cellStyle = styleDefault
					content = ed.getCell(dataRow, c)
				}

				if isCur && ed.mode == ModeInsert {
					ed.adjustEditOffset(w)
					ed.drawEditCell(cx, y, w, cellStyle)
				} else {
					ed.drawText(cx, y, w, cellStyle, truncPad(content, w))
				}
			} else {
				ed.drawText(cx, y, w, styleDefault, strings.Repeat(" ", w))
			}

			cx += w
			if cx < sw {
				// Match separator colour to the frozen header row.
				colSepStyle := styleSep
				if ed.headerRow && dataRow == 0 {
					colSepStyle = styleHdrRow
				}
				ed.putChar(cx, y, '│', colSepStyle)
				cx++
			}
		}
	}

	// drawHdrSep renders the frozen-header separator at screen y.
	drawHdrSep := func(y int) {
		ed.drawText(0, y, rowNumWidth, styleHdrRow, strings.Repeat("─", rowNumWidth))
		ed.putChar(rowNumWidth, y, '┼', styleHdrRow)
		hx := rowNumWidth + 1
		for c := ed.viewCol; c < ed.numCols() && hx < sw; c++ {
			w := ed.colWidths[c]
			if hx+w > sw {
				w = sw - hx
			}
			ed.drawText(hx, y, w, styleHdrRow, strings.Repeat("─", w))
			hx += w
			if hx < sw {
				ed.putChar(hx, y, '┼', styleHdrRow)
				hx++
			}
		}
	}

	if ed.headerRow {
		// Frozen header: row 0 always at y=2, separator at y=3, scrollable body at y=4+.
		drawRow(0, 2)
		drawHdrSep(3)
		dataH := sh - 4 // col-hdr(0) + col-border(1) + frozen-hdr(2) + hdr-sep(3) + status(sh-1)
		if dataH < 1 {
			dataH = 1
		}
		for sr := 0; sr < dataH; sr++ {
			drawRow(ed.viewRow+sr, sr+4)
		}
	} else {
		dataH := sh - 3 // col-hdr(0) + col-border(1) + status(sh-1)
		if dataH < 1 {
			dataH = 1
		}
		for sr := 0; sr < dataH; sr++ {
			drawRow(ed.viewRow+sr, sr+2)
		}
	}

	// ── Status bar (last row) ──
	statusY := sh - 1
	var statusText string
	var sStyle tcell.Style

	modFlag := ""
	if ed.modified {
		modFlag = "[+] "
	}
	if ed.headerRow {
		modFlag += "[HDR] "
	}

	switch ed.mode {
	case ModeCommand:
		// Show the command being typed; cursor is positioned after it.
		statusText = ":" + ed.cmdBuf
		sStyle = styleCmd
		ed.screen.ShowCursor(1+len([]rune(ed.cmdBuf)), statusY)

	case ModeInsert:
		cell := fmt.Sprintf("%s%d", colName(ed.curCol), ed.curRow+1)
		statusText = fmt.Sprintf(" INSERT  %s%-16s  %s  %s", modFlag, ed.filename, cell, ed.status)
		sStyle = styleStatus
		// Cursor is drawn inside the cell by drawEditCell.

	case ModeVisual:
		lo, hi := ed.selectionRange()
		statusText = fmt.Sprintf(" VISUAL  %s%-16s  rows %d–%d  %s", modFlag, ed.filename, lo+1, hi+1, ed.status)
		sStyle = styleStatus
		ed.screen.HideCursor()

	case ModeVisualCol:
		lo, hi := ed.selectionColRange()
		statusText = fmt.Sprintf(" VISUAL COL  %s%-16s  cols %s–%s  %s", modFlag, ed.filename, colName(lo), colName(hi), ed.status)
		sStyle = styleStatus
		ed.screen.HideCursor()

	default: // ModeNormal
		cell := fmt.Sprintf("%s%d", colName(ed.curCol), ed.curRow+1)
		pending := ""
		switch {
		case ed.gPressed:
			pending = "g"
		case ed.dPressed:
			pending = "d"
		case ed.yPressed:
			pending = "y"
		case ed.sPressed:
			pending = "s"
		case ed.cPressed:
			pending = "c"
		case ed.rPressed:
			pending = "r"
		}
		statusText = fmt.Sprintf(" NORMAL  %s%-16s  %s  %s%s", modFlag, ed.filename, cell, pending, ed.status)
		sStyle = styleStatus
		ed.screen.HideCursor()
	}

	// Pad to full screen width.
	runes := []rune(statusText)
	if len(runes) < sw {
		statusText += strings.Repeat(" ", sw-len(runes))
	}
	ed.drawText(0, statusY, sw, sStyle, statusText)

	ed.screen.Show()
}

// drawEditCell renders a cell in insert mode with horizontal scrolling.
// It shows editBuf[editOffset : editOffset+maxW] and places the terminal
// cursor at the position corresponding to editCur within that window.
func (ed *Editor) drawEditCell(x, y, maxW int, style tcell.Style) {
	runes := []rune(ed.editBuf)
	for i := 0; i < maxW; i++ {
		r := rune(' ')
		if idx := ed.editOffset + i; idx < len(runes) {
			r = runes[idx]
		}
		ed.screen.SetContent(x+i, y, r, nil, style)
	}
	ed.screen.ShowCursor(x+ed.editCur-ed.editOffset, y)
}

// adjustEditOffset keeps editCur visible within the cell's display window of width w.
func (ed *Editor) adjustEditOffset(w int) {
	if ed.editCur < ed.editOffset {
		ed.editOffset = ed.editCur
	}
	if ed.editCur >= ed.editOffset+w {
		ed.editOffset = ed.editCur - w + 1
	}
}

// ── String helpers ────────────────────────────────────────────────────────────

// truncPad truncates s to maxW runes (appending '>' if truncated) and pads to maxW with spaces.
func truncPad(s string, maxW int) string {
	runes := []rune(s)
	if len(runes) <= maxW {
		return s + strings.Repeat(" ", maxW-len(runes))
	}
	if maxW <= 1 {
		return strings.Repeat(">", maxW)
	}
	return string(runes[:maxW-1]) + ">"
}

// centerPad centres s in a field of width w, padding with spaces.
func centerPad(s string, w int) string {
	runes := []rune(s)
	if len(runes) >= w {
		return string(runes[:w])
	}
	pad := w - len(runes)
	left := pad / 2
	return strings.Repeat(" ", left) + s + strings.Repeat(" ", pad-left)
}

// ── Key handling ──────────────────────────────────────────────────────────────

func (ed *Editor) handleKey(ev *tcell.EventKey) bool {
	switch ed.mode {
	case ModeNormal:
		return ed.handleNormalKey(ev)
	case ModeInsert:
		ed.handleInsertKey(ev)
	case ModeCommand:
		return ed.handleCommandKey(ev)
	case ModeVisual:
		ed.handleVisualKey(ev)
	case ModeVisualCol:
		ed.handleVisualColKey(ev)
	}
	return false
}

func (ed *Editor) handleNormalKey(ev *tcell.EventKey) bool {
	key := ev.Key()
	ch := ev.Rune()

	// Escape and Ctrl+C cancel any pending multi-key sequence.
	if key == tcell.KeyEsc || key == tcell.KeyCtrlC {
		ed.gPressed, ed.dPressed, ed.yPressed, ed.sPressed = false, false, false, false
		ed.cPressed, ed.rPressed = false, false
		ed.status = ""
		return false
	}

	// ── Multi-key: gg ──
	if ed.gPressed {
		ed.gPressed = false
		ed.status = ""
		if ch == 'g' {
			ed.curRow = 0
			ed.clampCursor()
		}
		return false
	}

	// ── Multi-key: dd ──
	if ed.dPressed {
		ed.dPressed = false
		ed.status = ""
		switch ch {
		case 'd':
			rowCopy := make([]string, len(ed.data[ed.curRow]))
			copy(rowCopy, ed.data[ed.curRow])
			ed.yankRows = [][]string{rowCopy}
			ed.yankIsCol = false
			ed.pushUndo()
			ed.deleteRow(ed.curRow)
			ed.clampCursor()
			ed.status = "1 row deleted (yanked)"
		case 'c':
			ed.yankCols = [][]string{ed.getColumn(ed.curCol)}
			ed.yankIsCol = true
			ed.pushUndo()
			ed.deleteColumn(ed.curCol)
			ed.clampCursor()
			ed.status = "1 col deleted (yanked)"
		}
		return false
	}

	// ── Multi-key: yy / yc ──
	if ed.yPressed {
		ed.yPressed = false
		ed.status = ""
		switch ch {
		case 'y':
			if ed.curRow < len(ed.data) {
				rowCopy := make([]string, len(ed.data[ed.curRow]))
				copy(rowCopy, ed.data[ed.curRow])
				ed.yankRows = [][]string{rowCopy}
				ed.yankIsCol = false
				ed.status = "1 row yanked"
			}
		case 'c':
			ed.yankCols = [][]string{ed.getColumn(ed.curCol)}
			ed.yankIsCol = true
			ed.status = "1 col yanked"
		}
		return false
	}

	// ── Multi-key: sj / sk / s (toggle) ──
	if ed.sPressed {
		ed.sPressed = false
		ed.status = ""
		col := ed.curCol
		switch ch {
		case 'j': // force descending
			ed.pushUndo()
			ed.sortByCol(col, true)
			ed.status = fmt.Sprintf("Sorted by %s ↓", colName(col))
		case 'k': // force ascending
			ed.pushUndo()
			ed.sortByCol(col, false)
			ed.status = fmt.Sprintf("Sorted by %s ↑", colName(col))
		default: // toggle: any other key triggers the sort and is consumed
			// Sort ascending unless already sorted ascending by this col, then flip to descending.
			desc := ed.sortCol == col && !ed.sortDesc
			ed.pushUndo()
			ed.sortByCol(col, desc)
			if desc {
				ed.status = fmt.Sprintf("Sorted by %s ↓", colName(col))
			} else {
				ed.status = fmt.Sprintf("Sorted by %s ↑", colName(col))
			}
		}
		return false
	}

	// ── Multi-key: ca / ci (column insert) ──
	if ed.cPressed {
		ed.cPressed = false
		ed.status = ""
		switch ch {
		case 'a': // add column to the right
			ed.pushUndo()
			ed.insertColumnAt(ed.curCol+1, make([]string, ed.numRows()))
			ed.curCol++
			ed.computeColWidths()
			ed.status = "Column inserted right"
		case 'i': // insert column to the left
			ed.pushUndo()
			ed.insertColumnAt(ed.curCol, make([]string, ed.numRows()))
			ed.computeColWidths()
			ed.status = "Column inserted left"
		}
		return false
	}

	// ── Multi-key: ra / ri (row insert) ──
	if ed.rPressed {
		ed.rPressed = false
		ed.status = ""
		switch ch {
		case 'a': // add row below
			ed.pushUndo()
			ed.insertRowAt(ed.curRow+1, make([]string, ed.numCols()))
			ed.curRow++
			ed.status = "Row inserted below"
		case 'i': // insert row above
			ed.pushUndo()
			ed.insertRowAt(ed.curRow, make([]string, ed.numCols()))
			ed.status = "Row inserted above"
		}
		return false
	}

	// Clear status message on each fresh key press.
	ed.status = ""

	// ── Special (non-rune) keys ──
	switch key {
	case tcell.KeyCtrlQ:
		if ed.modified {
			ed.status = "Unsaved changes — use :wq to save or :q! to discard"
			return false
		}
		return true

	case tcell.KeyCtrlL: // redraw
		ed.screen.Sync()

	case tcell.KeyCtrlR: // redo
		ed.redo()

	case tcell.KeyCtrlF: // page down
		_, sh := ed.screen.Size()
		ed.curRow += sh - 2
		ed.clampCursor()
	case tcell.KeyCtrlB: // page up
		_, sh := ed.screen.Size()
		ed.curRow -= sh - 2
		ed.clampCursor()
	case tcell.KeyCtrlD: // half-page down
		_, sh := ed.screen.Size()
		ed.curRow += (sh - 2) / 2
		ed.clampCursor()
	case tcell.KeyCtrlU: // half-page up
		_, sh := ed.screen.Size()
		ed.curRow -= (sh - 2) / 2
		ed.clampCursor()

	case tcell.KeyUp:
		ed.curRow--
		ed.clampCursor()
	case tcell.KeyDown:
		ed.curRow++
		ed.clampCursor()
	case tcell.KeyLeft:
		ed.curCol--
		ed.clampCursor()
	case tcell.KeyRight:
		ed.curCol++
		ed.clampCursor()

	case tcell.KeyEnter:
		ed.enterInsertMode(false)

	case tcell.KeyTab: // Tab moves right without entering insert mode
		ed.curCol++
		ed.clampCursor()
	}

	// ── Rune keys ──
	switch ch {
	// Movement
	case 'h':
		ed.curCol--
		ed.clampCursor()
	case 'l':
		ed.curCol++
		ed.clampCursor()
	case 'j':
		ed.curRow++
		ed.clampCursor()
	case 'k':
		ed.curRow--
		ed.clampCursor()
	case 'w': // next column (word-forward analogue)
		ed.curCol++
		ed.clampCursor()
	case 'b': // prev column (word-back analogue)
		ed.curCol--
		ed.clampCursor()
	case '0', '^': // first column
		ed.curCol = 0
	case '$': // last column
		ed.curCol = ed.numCols() - 1
		ed.clampCursor()
	case 'g': // first key of 'gg'
		ed.gPressed = true
		ed.status = "g"
	case 'G': // last row
		ed.curRow = ed.numRows() - 1
		ed.clampCursor()

	// Edit mode entry
	case 'i':
		ed.enterInsertMode(false) // cursor at start
	case 'a', 'A':
		ed.enterInsertMode(true) // cursor at end (append)

	// Undo / redo
	case 'u':
		ed.undo()

	// Cell operations
	case 'x': // clear cell content
		if ed.getCell(ed.curRow, ed.curCol) != "" {
			ed.pushUndo()
			ed.setCell(ed.curRow, ed.curCol, "")
			ed.computeColWidths()
			ed.status = "Cell cleared"
		}

	// Row operations
	case 'd': // first key of 'dd'
		ed.dPressed = true
		ed.status = "d"
	case 'y': // first key of 'yy'
		ed.yPressed = true
		ed.status = "y"
	case 'p': // paste below (rows) or to the right (columns)
		if ed.yankIsCol && len(ed.yankCols) > 0 {
			ed.pushUndo()
			for i, col := range ed.yankCols {
				ed.insertColumnAt(ed.curCol+1+i, col)
			}
			ed.curCol += len(ed.yankCols)
			ed.status = fmt.Sprintf("%d col(s) pasted right", len(ed.yankCols))
		} else if len(ed.yankRows) > 0 {
			ed.pushUndo()
			for i, row := range ed.yankRows {
				newRow := make([]string, len(row))
				copy(newRow, row)
				ed.insertRowAt(ed.curRow+1+i, newRow)
			}
			ed.curRow += len(ed.yankRows)
			ed.status = fmt.Sprintf("%d row(s) pasted below", len(ed.yankRows))
		} else {
			ed.status = "Nothing in register"
		}
	case 'P': // paste above (rows) or to the left (columns)
		if ed.yankIsCol && len(ed.yankCols) > 0 {
			ed.pushUndo()
			for i, col := range ed.yankCols {
				ed.insertColumnAt(ed.curCol+i, col)
			}
			ed.status = fmt.Sprintf("%d col(s) pasted left", len(ed.yankCols))
		} else if len(ed.yankRows) > 0 {
			ed.pushUndo()
			for i, row := range ed.yankRows {
				newRow := make([]string, len(row))
				copy(newRow, row)
				ed.insertRowAt(ed.curRow+i, newRow)
			}
			ed.status = fmt.Sprintf("%d row(s) pasted above", len(ed.yankRows))
		} else {
			ed.status = "Nothing in register"
		}
	case 'o': // insert blank row below, enter insert mode
		ed.pushUndo()
		newRow := make([]string, ed.numCols())
		ed.insertRowAt(ed.curRow+1, newRow)
		ed.curRow++
		ed.enterInsertMode(false)
	case 'O': // insert blank row above, enter insert mode
		ed.pushUndo()
		newRow := make([]string, ed.numCols())
		ed.insertRowAt(ed.curRow, newRow)
		ed.enterInsertMode(false)

	// Visual modes
	case 'V':
		ed.mode = ModeVisual
		ed.anchorRow = ed.curRow
		ed.status = ""
	case 'v':
		ed.mode = ModeVisualCol
		ed.anchorCol = ed.curCol
		ed.status = ""

	// Column insert (ca=right, ci=left)
	case 'c':
		ed.cPressed = true
		ed.status = "c"

	// Row insert (ra=below, ri=above)
	case 'r':
		ed.rPressed = true
		ed.status = "r"

	// Sort (s alone toggles; sj=desc, sk=asc handled via sPressed)
	case 's':
		ed.sPressed = true
		ed.status = "s"

	// Command mode
	case ':':
		ed.mode = ModeCommand
		ed.cmdBuf = ""
	}

	return false
}

// selectionRange returns the top and bottom row indices of the visual selection.
func (ed *Editor) selectionRange() (lo, hi int) {
	if ed.curRow < ed.anchorRow {
		return ed.curRow, ed.anchorRow
	}
	return ed.anchorRow, ed.curRow
}

func (ed *Editor) handleVisualKey(ev *tcell.EventKey) {
	key := ev.Key()
	ch := ev.Rune()

	if ed.gPressed {
		ed.gPressed = false
		if ch == 'g' {
			ed.curRow = 0
			ed.clampCursor()
		}
		return
	}

	_, sh := ed.screen.Size()
	switch key {
	case tcell.KeyEsc, tcell.KeyCtrlC:
		ed.mode = ModeNormal
		ed.status = ""
		return
	case tcell.KeyUp:
		ed.curRow--
		ed.clampCursor()
		return
	case tcell.KeyDown:
		ed.curRow++
		ed.clampCursor()
		return
	case tcell.KeyCtrlU:
		ed.curRow -= (sh - 2) / 2
		ed.clampCursor()
		return
	case tcell.KeyCtrlD:
		ed.curRow += (sh - 2) / 2
		ed.clampCursor()
		return
	case tcell.KeyCtrlF:
		ed.curRow += sh - 2
		ed.clampCursor()
		return
	case tcell.KeyCtrlB:
		ed.curRow -= sh - 2
		ed.clampCursor()
		return
	}

	switch ch {
	case 'k':
		ed.curRow--
		ed.clampCursor()
	case 'j':
		ed.curRow++
		ed.clampCursor()
	case 'g':
		// Wait for second 'g' — reuse gPressed flag.
		ed.gPressed = true
	case 'G':
		ed.curRow = ed.numRows() - 1
		ed.clampCursor()

	case 'y': // yank selection
		lo, hi := ed.selectionRange()
		ed.yankRows = make([][]string, hi-lo+1)
		for i, row := range ed.data[lo : hi+1] {
			rowCopy := make([]string, len(row))
			copy(rowCopy, row)
			ed.yankRows[i] = rowCopy
		}
		ed.yankIsCol = false
		ed.mode = ModeNormal
		ed.status = fmt.Sprintf("%d row(s) yanked", hi-lo+1)

	case 'd': // delete selection (cut)
		lo, hi := ed.selectionRange()
		ed.yankRows = make([][]string, hi-lo+1)
		for i, row := range ed.data[lo : hi+1] {
			rowCopy := make([]string, len(row))
			copy(rowCopy, row)
			ed.yankRows[i] = rowCopy
		}
		ed.yankIsCol = false
		ed.pushUndo()
		// Delete from bottom up to keep indices stable.
		for r := hi; r >= lo; r-- {
			ed.deleteRow(r)
		}
		ed.clampCursor()
		ed.mode = ModeNormal
		ed.status = fmt.Sprintf("%d row(s) deleted (yanked)", hi-lo+1)
	}
}

// selectionColRange returns the left and right column indices of the visual column selection.
func (ed *Editor) selectionColRange() (lo, hi int) {
	if ed.curCol < ed.anchorCol {
		return ed.curCol, ed.anchorCol
	}
	return ed.anchorCol, ed.curCol
}

func (ed *Editor) handleVisualColKey(ev *tcell.EventKey) {
	key := ev.Key()
	ch := ev.Rune()

	switch key {
	case tcell.KeyEsc, tcell.KeyCtrlC:
		ed.mode = ModeNormal
		ed.status = ""
		return
	case tcell.KeyLeft:
		ed.curCol--
		ed.clampCursor()
		return
	case tcell.KeyRight:
		ed.curCol++
		ed.clampCursor()
		return
	}

	switch ch {
	case 'h':
		ed.curCol--
		ed.clampCursor()
	case 'l':
		ed.curCol++
		ed.clampCursor()
	case '0', '^':
		ed.curCol = 0
	case '$':
		ed.curCol = ed.numCols() - 1
		ed.clampCursor()

	case 'y': // yank selected columns
		lo, hi := ed.selectionColRange()
		ed.yankCols = make([][]string, hi-lo+1)
		for i := lo; i <= hi; i++ {
			ed.yankCols[i-lo] = ed.getColumn(i)
		}
		ed.yankIsCol = true
		ed.mode = ModeNormal
		ed.status = fmt.Sprintf("%d col(s) yanked", hi-lo+1)

	case 'd': // cut selected columns
		lo, hi := ed.selectionColRange()
		ed.yankCols = make([][]string, hi-lo+1)
		for i := lo; i <= hi; i++ {
			ed.yankCols[i-lo] = ed.getColumn(i)
		}
		ed.yankIsCol = true
		ed.pushUndo()
		// Delete from right to left to keep indices stable.
		for c := hi; c >= lo; c-- {
			ed.deleteColumn(c)
		}
		ed.clampCursor()
		ed.mode = ModeNormal
		ed.status = fmt.Sprintf("%d col(s) deleted (yanked)", hi-lo+1)
	}
}

// enterInsertMode switches to insert mode.
// If atEnd is true, the edit cursor starts at the end of the cell content.
func (ed *Editor) enterInsertMode(atEnd bool) {
	ed.mode = ModeInsert
	ed.origCell = ed.getCell(ed.curRow, ed.curCol)
	ed.editBuf = ed.origCell
	ed.editOffset = 0
	if atEnd {
		ed.editCur = len([]rune(ed.editBuf))
	} else {
		ed.editCur = 0
	}
	ed.status = ""
}

func (ed *Editor) handleInsertKey(ev *tcell.EventKey) {
	// Alt+F / Alt+B: word forward / backward.
	if ev.Modifiers()&tcell.ModAlt != 0 {
		runes := []rune(ed.editBuf)
		switch ev.Rune() {
		case 'f', 'F':
			ed.editCur = wordForward(runes, ed.editCur)
			ed.adjustEditOffset(ed.colWidths[ed.curCol])
			return
		case 'b', 'B':
			ed.editCur = wordBackward(runes, ed.editCur)
			ed.adjustEditOffset(ed.colWidths[ed.curCol])
			return
		}
	}

	key := ev.Key()

	switch key {
	case tcell.KeyEsc, tcell.KeyCtrlC:
		// Accept changes and return to normal mode.
		if ed.editBuf != ed.origCell {
			ed.pushUndo()
		}
		ed.commitEdit()
		ed.mode = ModeNormal

	case tcell.KeyEnter:
		if ed.editBuf != ed.origCell {
			ed.pushUndo()
		}
		ed.commitEdit()
		ed.mode = ModeNormal
		ed.curRow++
		ed.clampCursor()

	case tcell.KeyTab:
		if ed.editBuf != ed.origCell {
			ed.pushUndo()
		}
		ed.commitEdit()
		ed.mode = ModeNormal
		ed.curCol++
		if ed.curCol >= ed.numCols() {
			ed.curCol = 0
			ed.curRow++
		}
		ed.clampCursor()

	case tcell.KeyBackspace, tcell.KeyBackspace2:
		runes := []rune(ed.editBuf)
		if ed.editCur > 0 {
			ed.editBuf = string(runes[:ed.editCur-1]) + string(runes[ed.editCur:])
			ed.editCur--
			ed.adjustEditOffset(ed.colWidths[ed.curCol])
		}

	case tcell.KeyDelete, tcell.KeyCtrlD: // delete char forward
		runes := []rune(ed.editBuf)
		if ed.editCur < len(runes) {
			ed.editBuf = string(runes[:ed.editCur]) + string(runes[ed.editCur+1:])
		}

	case tcell.KeyLeft, tcell.KeyCtrlB: // back one char
		if ed.editCur > 0 {
			ed.editCur--
			ed.adjustEditOffset(ed.colWidths[ed.curCol])
		}
	case tcell.KeyRight, tcell.KeyCtrlF: // forward one char
		if ed.editCur < len([]rune(ed.editBuf)) {
			ed.editCur++
			ed.adjustEditOffset(ed.colWidths[ed.curCol])
		}
	case tcell.KeyHome, tcell.KeyCtrlA: // beginning of cell
		ed.editCur = 0
		ed.editOffset = 0
	case tcell.KeyEnd, tcell.KeyCtrlE: // end of cell
		ed.editCur = len([]rune(ed.editBuf))
		ed.adjustEditOffset(ed.colWidths[ed.curCol])

	case tcell.KeyCtrlK: // kill to end of cell
		ed.editBuf = string([]rune(ed.editBuf)[:ed.editCur])

	case tcell.KeyCtrlW: // delete last word
		runes := []rune(ed.editBuf)
		pos := ed.editCur
		// skip trailing spaces
		for pos > 0 && runes[pos-1] == ' ' {
			pos--
		}
		// delete back to next space
		for pos > 0 && runes[pos-1] != ' ' {
			pos--
		}
		ed.editBuf = string(runes[:pos]) + string(runes[ed.editCur:])
		ed.editCur = pos
		ed.adjustEditOffset(ed.colWidths[ed.curCol])

	case tcell.KeyCtrlU: // delete from start to cursor
		runes := []rune(ed.editBuf)
		ed.editBuf = string(runes[ed.editCur:])
		ed.editCur = 0
		ed.editOffset = 0

	default:
		if r := ev.Rune(); r != 0 {
			runes := []rune(ed.editBuf)
			// Insert rune at editCur.
			newRunes := make([]rune, len(runes)+1)
			copy(newRunes, runes[:ed.editCur])
			newRunes[ed.editCur] = r
			copy(newRunes[ed.editCur+1:], runes[ed.editCur:])
			ed.editBuf = string(newRunes)
			ed.editCur++
			ed.adjustEditOffset(ed.colWidths[ed.curCol])
		}
	}
}

// commitEdit writes editBuf into the current cell.
func (ed *Editor) commitEdit() {
	ed.setCell(ed.curRow, ed.curCol, ed.editBuf)
}

func (ed *Editor) handleCommandKey(ev *tcell.EventKey) bool {
	key := ev.Key()

	switch key {
	case tcell.KeyEsc, tcell.KeyCtrlC:
		ed.mode = ModeNormal
		ed.cmdBuf = ""
		return false

	case tcell.KeyEnter:
		cmd := strings.TrimSpace(ed.cmdBuf)
		ed.mode = ModeNormal
		ed.cmdBuf = ""
		return ed.execCommand(cmd)

	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if len(ed.cmdBuf) > 0 {
			runes := []rune(ed.cmdBuf)
			ed.cmdBuf = string(runes[:len(runes)-1])
		} else {
			ed.mode = ModeNormal
		}

	default:
		if r := ev.Rune(); r != 0 {
			ed.cmdBuf += string(r)
		}
	}
	return false
}

func (ed *Editor) execCommand(cmd string) bool {
	switch cmd {
	case "w":
		if err := writeCSV(ed.filename, ed.data); err != nil {
			ed.status = fmt.Sprintf("Error saving: %v", err)
		} else {
			ed.modified = false
			ed.status = fmt.Sprintf("Saved %q", ed.filename)
		}
	case "q":
		if ed.modified {
			ed.status = "Unsaved changes — use :wq or :q!"
		} else {
			return true
		}
	case "q!":
		return true
	case "wq", "x":
		if err := writeCSV(ed.filename, ed.data); err != nil {
			ed.status = fmt.Sprintf("Error saving: %v", err)
		} else {
			return true
		}
	case "set header":
		ed.headerRow = true
		// Body viewport must not include the frozen header row.
		if ed.viewRow < 1 {
			ed.viewRow = 1
		}
		ed.status = "Header row enabled"
	case "set noheader":
		ed.headerRow = false
		ed.status = "Header row disabled"
	default:
		// :N — jump to row N
		var row int
		if n, _ := fmt.Sscanf(cmd, "%d", &row); n == 1 {
			ed.curRow = row - 1
			ed.clampCursor()
		} else {
			ed.status = fmt.Sprintf("Unknown command: %s", cmd)
		}
	}
	return false
}

// ── Row operations ────────────────────────────────────────────────────────────

func (ed *Editor) deleteRow(r int) {
	if r < 0 || r >= len(ed.data) {
		return
	}
	ed.data = append(ed.data[:r], ed.data[r+1:]...)
	if len(ed.data) == 0 {
		ed.data = [][]string{{"", "", ""}}
	}
	ed.modified = true
	ed.computeColWidths()
}

func (ed *Editor) insertRowAt(r int, row []string) {
	newRow := make([]string, len(row))
	copy(newRow, row)
	if r >= len(ed.data) {
		ed.data = append(ed.data, newRow)
	} else {
		ed.data = append(ed.data, nil)
		copy(ed.data[r+1:], ed.data[r:])
		ed.data[r] = newRow
	}
	ed.modified = true
}

// ── Sort ──────────────────────────────────────────────────────────────────────

func (ed *Editor) sortByCol(c int, descending bool) {
	if len(ed.data) == 0 {
		return
	}
	// Determine the slice to sort (skip row 0 if headerRow is set).
	start := 0
	if ed.headerRow && len(ed.data) > 1 {
		start = 1
	}
	rows := ed.data[start:]
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := "", ""
		if c < len(rows[i]) {
			a = rows[i][c]
		}
		if c < len(rows[j]) {
			b = rows[j][c]
		}
		if descending {
			return a > b
		}
		return a < b
	})
	ed.sortCol = c
	ed.sortDesc = descending
	ed.modified = true
}

// ── Column operations ─────────────────────────────────────────────────────────

// getColumn returns all cell values in column c (one entry per row).
func (ed *Editor) getColumn(c int) []string {
	vals := make([]string, len(ed.data))
	for i, row := range ed.data {
		if c < len(row) {
			vals[i] = row[c]
		}
	}
	return vals
}

func (ed *Editor) deleteColumn(c int) {
	if c < 0 || c >= ed.numCols() {
		return
	}
	for i, row := range ed.data {
		if c < len(row) {
			ed.data[i] = append(row[:c:c], row[c+1:]...)
		}
	}
	ed.colWidths = append(ed.colWidths[:c:c], ed.colWidths[c+1:]...)
	if len(ed.colWidths) == 0 {
		ed.colWidths = []int{minColWidth}
	}
	ed.modified = true
	ed.computeColWidths()
}

func (ed *Editor) insertColumnAt(c int, vals []string) {
	for i := range ed.data {
		val := ""
		if i < len(vals) {
			val = vals[i]
		}
		row := ed.data[i]
		// Pad row to at least c cells so the insert position is valid.
		for len(row) < c {
			row = append(row, "")
		}
		newRow := make([]string, len(row)+1)
		copy(newRow, row[:c])
		newRow[c] = val
		copy(newRow[c+1:], row[c:])
		ed.data[i] = newRow
	}
	// Insert a slot into colWidths at position c.
	newWidths := make([]int, len(ed.colWidths)+1)
	copy(newWidths, ed.colWidths[:c])
	newWidths[c] = minColWidth
	copy(newWidths[c+1:], ed.colWidths[c:])
	ed.colWidths = newWidths
	ed.computeColWidths()
	ed.modified = true
}

// ── Mouse handling ────────────────────────────────────────────────────────────

// cellAt converts screen coordinates to (row, col) in the data grid.
// Returns (-1, -1) if the position is outside the data area.
func (ed *Editor) cellAt(mx, my, sw, sh int) (row, col int) {
	// y=0 col-header, y=1 col-border, y=sh-1 status bar — all non-data.
	if my < 2 || my >= sh-1 {
		return -1, -1
	}
	if mx <= rowNumWidth {
		return -1, -1
	}
	if ed.headerRow {
		switch {
		case my == 2:
			row = 0 // frozen header row
		case my == 3:
			return -1, -1 // separator line, not a cell
		default:
			row = ed.viewRow + (my - 4)
		}
	} else {
		row = ed.viewRow + (my - 2)
	}
	if row >= ed.numRows() {
		return -1, -1
	}
	// Walk columns to find which one mx falls in.
	x := rowNumWidth + 1
	for c := ed.viewCol; c < ed.numCols() && x < sw; c++ {
		w := ed.colWidths[c]
		if x+w > sw {
			w = sw - x
		}
		if mx >= x && mx < x+w {
			return row, c
		}
		x += w + 1 // +1 for the separator character
	}
	return -1, -1
}

const doubleClickMs = 300

func (ed *Editor) handleMouse(ev *tcell.EventMouse, sw, sh int) {
	// Scroll wheel: move the viewport (and cursor) up/down.
	switch ev.Buttons() {
	case tcell.WheelUp:
		if ed.curRow > 0 {
			ed.curRow--
		}
		return
	case tcell.WheelDown:
		if ed.curRow < ed.numRows()-1 {
			ed.curRow++
		}
		return
	}

	// Only act on left-button press events.
	if ev.Buttons() != tcell.Button1 {
		return
	}

	mx, my := ev.Position()
	row, col := ed.cellAt(mx, my, sw, sh)
	if row < 0 {
		return
	}

	now := ev.When()
	isDouble := row == ed.lastClickRow &&
		col == ed.lastClickCol &&
		now.Sub(ed.lastClickTime) <= doubleClickMs*time.Millisecond

	ed.lastClickTime = now
	ed.lastClickRow = row
	ed.lastClickCol = col

	if ed.mode == ModeInsert {
		// Commit any in-progress edit before moving.
		if ed.editBuf != ed.origCell {
			ed.pushUndo()
		}
		ed.commitEdit()
		ed.mode = ModeNormal
	}

	ed.curRow = row
	ed.curCol = col

	if isDouble {
		ed.enterInsertMode(false)
	}
}

// ── Word navigation helpers ───────────────────────────────────────────────────

func isWordChar(r rune) bool {
	return r != ' ' && r != '\t'
}

// wordForward moves pos to the end of the next word (emacs Alt+F behaviour).
// Skips non-word chars, then skips word chars.
func wordForward(runes []rune, pos int) int {
	n := len(runes)
	for pos < n && !isWordChar(runes[pos]) {
		pos++
	}
	for pos < n && isWordChar(runes[pos]) {
		pos++
	}
	return pos
}

// wordBackward moves pos to the start of the previous word (emacs Alt+B behaviour).
// Skips non-word chars going left, then skips word chars going left.
func wordBackward(runes []rune, pos int) int {
	for pos > 0 && !isWordChar(runes[pos-1]) {
		pos--
	}
	for pos > 0 && isWordChar(runes[pos-1]) {
		pos--
	}
	return pos
}

// ── Undo / Redo ───────────────────────────────────────────────────────────────

func deepCopyData(data [][]string) [][]string {
	cp := make([][]string, len(data))
	for i, row := range data {
		cp[i] = make([]string, len(row))
		copy(cp[i], row)
	}
	return cp
}

// pushUndo saves the current state onto the undo stack and clears the redo stack.
// Call this immediately before any mutating operation.
func (ed *Editor) pushUndo() {
	ed.undoStack = append(ed.undoStack, snapshot{
		data:   deepCopyData(ed.data),
		curRow: ed.curRow,
		curCol: ed.curCol,
	})
	ed.redoStack = ed.redoStack[:0] // new action invalidates redo history
}

func (ed *Editor) undo() {
	if len(ed.undoStack) == 0 {
		ed.status = "Already at oldest change"
		return
	}
	// Save current state for redo (don't clear redo stack here).
	ed.redoStack = append(ed.redoStack, snapshot{
		data:   deepCopyData(ed.data),
		curRow: ed.curRow,
		curCol: ed.curCol,
	})
	snap := ed.undoStack[len(ed.undoStack)-1]
	ed.undoStack = ed.undoStack[:len(ed.undoStack)-1]
	ed.data = snap.data
	ed.curRow = snap.curRow
	ed.curCol = snap.curCol
	ed.computeColWidths()
	ed.modified = true
	ed.status = "Undone"
}

func (ed *Editor) redo() {
	if len(ed.redoStack) == 0 {
		ed.status = "Already at newest change"
		return
	}
	// Save current state for undo (don't clear redo stack here).
	ed.undoStack = append(ed.undoStack, snapshot{
		data:   deepCopyData(ed.data),
		curRow: ed.curRow,
		curCol: ed.curCol,
	})
	snap := ed.redoStack[len(ed.redoStack)-1]
	ed.redoStack = ed.redoStack[:len(ed.redoStack)-1]
	ed.data = snap.data
	ed.curRow = snap.curRow
	ed.curCol = snap.curCol
	ed.computeColWidths()
	ed.modified = true
	ed.status = "Redone"
}
