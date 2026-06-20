package main

import (
	"image/color"
	"strconv"

	"tinygo.org/x/tinydraw"
	"tinygo.org/x/tinyfont"
	"tinygo.org/x/tinyfont/proggy"
)

var (
	black = color.RGBA{0, 0, 0, 255}
	white = color.RGBA{255, 255, 255, 255}
	red   = color.RGBA{255, 0, 0, 255}
	font  = &proggy.TinySZ8pt7b
)

const (
	screenW = 296
	screenH = 128
	marginX = 8

	// Progress bar dimensions
	barW = 210 // width of the outlined bar frame
	barH = 16  // height of the bar

	// Big digit rendering (5x7 bitmap scaled 2x)
	digitScale = 2
	digitW     = 5 * digitScale
	digitH     = 7 * digitScale
	digitGap   = 2
)

// bigGlyphs: 5x7 bitmap font for digits 0-9 and % sign (index 10).
var bigGlyphs = [11][7]uint8{
	{0x0E, 0x11, 0x11, 0x11, 0x11, 0x11, 0x0E}, // 0
	{0x04, 0x0C, 0x04, 0x04, 0x04, 0x04, 0x0E}, // 1
	{0x0E, 0x11, 0x01, 0x02, 0x04, 0x08, 0x1F}, // 2
	{0x0E, 0x11, 0x01, 0x06, 0x01, 0x11, 0x0E}, // 3
	{0x02, 0x06, 0x0A, 0x12, 0x1F, 0x02, 0x02}, // 4
	{0x1F, 0x10, 0x1E, 0x01, 0x01, 0x11, 0x0E}, // 5
	{0x06, 0x08, 0x10, 0x1E, 0x11, 0x11, 0x0E}, // 6
	{0x1F, 0x01, 0x02, 0x04, 0x08, 0x08, 0x08}, // 7
	{0x0E, 0x11, 0x11, 0x0E, 0x11, 0x11, 0x0E}, // 8
	{0x0E, 0x11, 0x11, 0x0F, 0x01, 0x02, 0x0C}, // 9
	{0x18, 0x19, 0x02, 0x04, 0x08, 0x13, 0x03}, // % (index 10)
}

type UsageData struct {
	H5Used     int64
	H5Limit    int64
	W1Used     int64
	W1Limit    int64
	H5ResetMin int64 // minutes-of-day 0–1439, or -1 = unknown
	W1ResetDay int64 // weekday 0=Sun..6=Sat, or -1 = unknown
	W1ResetMin int64 // minutes-of-day 0–1439, or -1 = unknown
}

func usageChanged(a, b *UsageData) bool {
	return a.H5Used != b.H5Used ||
		a.H5Limit != b.H5Limit ||
		a.W1Used != b.W1Used ||
		a.W1Limit != b.W1Limit
}

func renderUsageScreen(d *EPD, u *UsageData) {
	d.ClearBuffer()

	// Header
	tinyfont.WriteLine(d, font, marginX, 12, "CLAUDE PRO", black)
	tinydraw.FilledRectangle(d, marginX, 16, screenW-2*marginX, 2, black)

	// 5-hour section in RED — reset as "HH:MM"
	h5reset := formatClock(u.H5ResetMin)
	drawSection(d, 26, "5-HOUR", u.H5Used, u.H5Limit, h5reset, red)

	// Divider
	drawDashedLine(d, marginX, 66, screenW-marginX)

	// Weekly section in BLACK — reset as "Ddd HH:MM"
	w1reset := ""
	if u.W1ResetDay >= 0 && u.W1ResetMin >= 0 {
		w1reset = weekdayAbbrev(u.W1ResetDay) + " " + formatClock(u.W1ResetMin)
	}
	drawSection(d, 74, "WEEKLY", u.W1Used, u.W1Limit, w1reset, black)
}

func drawSection(d *EPD, labelY int16, label string, used, limit int64, resetLabel string, col color.RGBA) {
	pct := int64(0)
	if limit > 0 {
		pct = (used * 100) / limit
	}
	if pct > 100 {
		pct = 100
	}

	// Label (left) and reset time (right)
	tinyfont.WriteLine(d, font, marginX, labelY, label, col)
	if resetLabel != "" {
		resetW := int16(len(resetLabel)) * 6
		tinyfont.WriteLine(d, font, screenW-marginX-resetW, labelY, resetLabel, col)
	}

	// Progress bar: outlined frame + solid fill
	barX := int16(marginX)
	barY := labelY + 4
	fillW := int16((int64(barW) * pct) / 100)

	// Outer frame
	tinydraw.Rectangle(d, barX, barY, int16(barW), int16(barH), col)

	// Filled portion (inset by 1px so fill is inside the frame)
	if fillW > 2 {
		tinydraw.FilledRectangle(d, barX+1, barY+1, fillW-2, int16(barH)-2, col)
	}

	// Big used-% number to the right of the bar
	pctX := barX + int16(barW) + 6
	pctY := barY + (int16(barH)-int16(digitH))/2
	drawBigPercent(d, pctX, pctY, pct, col)

	// Token detail line below bar
	detailY := barY + barH + 8
	tinyfont.WriteLine(d, font, marginX, detailY, formatTokens(used)+" / "+formatTokens(limit)+" tokens", col)
}

func drawBigPercent(d *EPD, x, y int16, value int64, col color.RGBA) {
	s := strconv.FormatInt(value, 10)
	cx := x
	for i := 0; i < len(s); i++ {
		idx := int(s[i] - '0')
		if idx >= 0 && idx <= 9 {
			drawBigGlyph(d, cx, y, idx, col)
			cx += int16(digitW + digitGap)
		}
	}
	drawBigGlyph(d, cx, y, 10, col) // % sign
}

func drawBigGlyph(d *EPD, x, y int16, idx int, col color.RGBA) {
	for row := 0; row < 7; row++ {
		bits := bigGlyphs[idx][row]
		for c := 0; c < 5; c++ {
			if bits&(0x10>>uint(c)) != 0 {
				px := x + int16(c*digitScale)
				py := y + int16(row*digitScale)
				for dx := int16(0); dx < int16(digitScale); dx++ {
					for dy := int16(0); dy < int16(digitScale); dy++ {
						d.SetPixel(px+dx, py+dy, col)
					}
				}
			}
		}
	}
}

func renderErrorScreen(d *EPD) {
	d.ClearBuffer()
	tinyfont.WriteLine(d, font, marginX, 35, "ERROR", red)
	tinydraw.FilledRectangle(d, marginX, 39, 30, 2, red)
	tinyfont.WriteLine(d, font, marginX, 58, "Token expired!", black)
	tinyfont.WriteLine(d, font, marginX, 80, "Run 'claude' to refresh,", black)
	tinyfont.WriteLine(d, font, marginX, 96, "then restart clue.", black)
}

func renderSetupScreen(d *EPD) {
	d.ClearBuffer()
	tinyfont.WriteLine(d, font, marginX, 30, "CLUE READY", black)
	tinydraw.FilledRectangle(d, marginX, 34, 60, 2, black)
	tinyfont.WriteLine(d, font, marginX, 54, "Claude Usage E-Ink Display", black)
	tinyfont.WriteLine(d, font, marginX, 76, "Waiting for host daemon.", black)
	tinyfont.WriteLine(d, font, marginX, 96, "Run: ./clue", black)
}

func renderConnectingScreen(d *EPD) {
	d.ClearBuffer()
	tinyfont.WriteLine(d, font, marginX, 50, "CONNECTING...", black)
	tinydraw.FilledRectangle(d, marginX, 54, 78, 2, black)
	tinyfont.WriteLine(d, font, marginX, 74, "Waiting for clue daemon.", black)
}

func drawDashedLine(d *EPD, x1, y, x2 int16) {
	for x := x1; x < x2; x += 6 {
		end := x + 3
		if end > x2 {
			end = x2
		}
		tinydraw.Line(d, x, y, end, y, black)
	}
}

func formatTokens(n int64) string {
	if n >= 1_000_000_000 {
		return strconv.FormatInt(n/1_000_000_000, 10) + "." + strconv.FormatInt((n%1_000_000_000)/100_000_000, 10) + "B"
	}
	if n >= 1_000_000 {
		return strconv.FormatInt(n/1_000_000, 10) + "." + strconv.FormatInt((n%1_000_000)/100_000, 10) + "M"
	}
	if n >= 1_000 {
		return strconv.FormatInt(n/1_000, 10) + "." + strconv.FormatInt((n%1_000)/100, 10) + "K"
	}
	return strconv.FormatInt(n, 10)
}

// formatClock formats a minute-of-day (0–1439) as "HH:MM". Returns "" if min < 0.
func formatClock(min int64) string {
	if min < 0 {
		return ""
	}
	h := min / 60
	m := min % 60
	hStr := strconv.FormatInt(h, 10)
	mStr := strconv.FormatInt(m, 10)
	if h < 10 {
		hStr = "0" + hStr
	}
	if m < 10 {
		mStr = "0" + mStr
	}
	return hStr + ":" + mStr
}

// weekdayAbbrev returns a 3-letter weekday abbreviation for day 0=Sun..6=Sat.
func weekdayAbbrev(day int64) string {
	names := [7]string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}
	if day < 0 || day > 6 {
		return "???"
	}
	return names[day]
}

// renderCalibration draws a 1-px border around the full 296×128 screen area
// plus 10-px corner ticks for measuring physical panel offset.
func renderCalibration(d *EPD) {
	d.ClearBuffer()
	// Outer border
	tinydraw.Rectangle(d, 0, 0, screenW, screenH, black)
	// Corner ticks (10px long, 1px wide)
	for i := int16(0); i < 10; i++ {
		// Top-left
		d.SetPixel(i, 1, red)
		d.SetPixel(1, i, red)
		// Top-right
		d.SetPixel(screenW-1-i, 1, red)
		d.SetPixel(screenW-2, i, red)
		// Bottom-left
		d.SetPixel(i, screenH-2, red)
		d.SetPixel(1, screenH-1-i, red)
		// Bottom-right
		d.SetPixel(screenW-1-i, screenH-2, red)
		d.SetPixel(screenW-2, screenH-1-i, red)
	}
	tinyfont.WriteLine(d, font, 10, 20, "CALIBRATION", black)
	tinyfont.WriteLine(d, font, 10, 36, "Border must be flush", black)
	tinyfont.WriteLine(d, font, 10, 52, "with all 4 edges.", black)
}
