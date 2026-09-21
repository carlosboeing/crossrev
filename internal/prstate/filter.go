package prstate

// FilterGeneration runs the publication filter over every free-text field
// a generation carries: the scope report, the record reasons, evidence and
// finding ids, and the advisory and exclusion reasons.
//
// Both stores call it before encoding, so the digests describe the bytes
// the forge actually persists. A store that digested first and filtered
// later — the marker comment passes through CommentEdit's filter after the
// payload is embedded — would persist a generation that never verifies.
// An error aborts the publication: a generation the filter could not
// process must not be written.
//
// It rewrites the generation in place.
func FilterGeneration(filter func(string) (string, error), g *Generation) error {
	filterOpt := func(o Opt[string]) (Opt[string], error) {
		if !o.Present() {
			return o, nil
		}
		s, _ := o.Get()
		filtered, err := filter(s)
		if err != nil {
			return o, err
		}
		return Some(filtered), nil
	}

	filterStr := func(s string) (string, error) {
		return filter(s)
	}

	var err error
	g.ScopeReport.ExaminedScope, err = filterStr(g.ScopeReport.ExaminedScope)
	if err != nil {
		return err
	}

	for i := range g.ScopeReport.KnownLimits {
		g.ScopeReport.KnownLimits[i], err = filterStr(g.ScopeReport.KnownLimits[i])
		if err != nil {
			return err
		}
	}

	for i := range g.Records {
		r := &g.Records[i]
		r.Reason, err = filterOpt(r.Reason)
		if err != nil {
			return err
		}
		for j := range r.Evidence {
			ev := &r.Evidence[j]
			ev.Source, err = filterStr(ev.Source)
			if err != nil {
				return err
			}
			ev.Note, err = filterOpt(ev.Note)
			if err != nil {
				return err
			}
		}
		for j := range r.FindingIDs {
			r.FindingIDs[j], err = filterStr(r.FindingIDs[j])
			if err != nil {
				return err
			}
		}
	}

	for i := range g.Advisory.Rules {
		g.Advisory.Rules[i], err = filterStr(g.Advisory.Rules[i])
		if err != nil {
			return err
		}
	}
	for i := range g.Advisory.Limits {
		lim := &g.Advisory.Limits[i]
		lim.Reason, err = filterStr(lim.Reason)
		if err != nil {
			return err
		}
	}

	for i := range g.Excluded {
		ex := &g.Excluded[i]
		ex.Reason, err = filterStr(ex.Reason)
		if err != nil {
			return err
		}
	}

	return nil
}
