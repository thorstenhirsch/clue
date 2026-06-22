package main

func updateUsage(d *EPD, lastUsage *UsageData, u UsageData) {
	if barTurnsRed(lastUsage, &u) {
		d.ForceFullNext = true
	}
	blinks := blinkCount(lastUsage, &u)
	*lastUsage = u
	renderUsageScreen(d, &u)
	d.RefreshSmart()
	if blinks > 0 {
		blink(blinks)
	}
}

func showError(d *EPD) {
	renderErrorScreen(d)
	d.RefreshSmart()
}
