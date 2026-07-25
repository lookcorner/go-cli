package tui

type inputClearDetector struct {
	peak int
	last int
}

func (d *inputClearDetector) observeUserEdit(before, after int) bool {
	if before != d.last {
		d.peak = before
	}
	fired := d.peak >= 20 && after <= 5
	if fired {
		d.peak = after
	} else {
		d.peak = max(d.peak, after)
	}
	d.last = after
	return fired
}
