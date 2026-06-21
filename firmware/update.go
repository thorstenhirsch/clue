package main

func updateUsage(d *EPD, lastUsage *UsageData, u UsageData) {
	if barTurnsRed(lastUsage, &u) {
		d.ForceFullNext = true
	}
	*lastUsage = u
	renderUsageScreen(d, &u)
	d.RefreshSmart()
}

func showError(d *EPD) {
	renderErrorScreen(d)
	d.RefreshSmart()
}
