package firefoxaddons

import (
	"fmt"
	"sort"
	"strings"

	"github.com/homielab/homielab-mcp/internal/render"
)

func date(s string) string {
	if len(s) >= 10 {
		return s[:10]
	}
	return render.OrDash(s)
}

func fileStatus(v *addonVersion) string {
	if v == nil || v.File == nil {
		return "-"
	}
	return render.OrDash(rawText(v.File.Status))
}

func compat(m map[string]appRange) string {
	apps := make([]string, 0, len(m))
	for app, r := range m {
		apps = append(apps, fmt.Sprintf("%s %s–%s", app, r.Min, r.Max))
	}
	sort.Strings(apps)
	return strings.Join(apps, ", ")
}

func nextPage(next *string, page int) string {
	if next == nil || *next == "" {
		return ""
	}
	if page < 1 {
		page = 1
	}
	return fmt.Sprintf("\nMore: page %d\n", page+1)
}

func renderAddonList(out page[addon], pg int, mine bool) string {
	if len(out.Results) == 0 {
		return "No add-ons match."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d add-on(s)", out.Count)
	if out.Count > len(out.Results) {
		fmt.Fprintf(&b, ", showing %d", len(out.Results))
	}
	b.WriteString(":\n\n| id | slug | name | type | status | version | users | weekly dl | rating |")
	if mine {
		b.WriteString(" channels | guid |")
	}
	b.WriteString("\n|---:|---|---|---|---|---|---:|---:|---|")
	if mine {
		b.WriteString("---|---|")
	}
	b.WriteString("\n")
	for _, a := range out.Results {
		ver := "-"
		if a.CurrentVersion != nil {
			ver = a.CurrentVersion.Version
		}
		status := a.Status
		if a.IsDisabled {
			status += " (disabled)"
		}
		fmt.Fprintf(&b, "| %d | %s | %s | %s | %s | %s | %d | %d | %.2f★ (%d) |", a.ID, render.MDCell(a.Slug), render.MDCell(a.Name.pick(a.DefaultLocale)),
			a.Type, render.OrDash(status), render.MDCell(ver), a.AverageDailyUsers, a.WeeklyDownloads, a.Ratings.Average, a.Ratings.Count)
		if mine {
			fmt.Fprintf(&b, " %s | %s |", render.OrDash(strings.Join(a.Channels, ", ")), render.MDCell(a.GUID))
		}
		b.WriteString("\n")
	}
	b.WriteString(nextPage(out.Next, pg))
	return b.String()
}

func renderAddon(a addon) string {
	loc := a.DefaultLocale
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n", a.Name.pick(loc))
	fmt.Fprintf(&b, "id %d · slug %s · guid %s · %s\n", a.ID, a.Slug, render.OrDash(a.GUID), a.Type)
	status := a.Status
	if a.IsDisabled {
		status += ", DISABLED"
	}
	if a.IsExperimental {
		status += ", experimental"
	}
	if a.RequiresPayment {
		status += ", requires payment"
	}
	fmt.Fprintf(&b, "Status: %s", render.OrDash(status))
	if len(a.Channels) > 0 {
		fmt.Fprintf(&b, " · channels: %s", strings.Join(a.Channels, ", "))
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "Summary: %s\n", render.OrDash(a.Summary.pick(loc)))
	fmt.Fprintf(&b, "Default locale: %s · name in %s · description in %s\n", render.OrDash(loc),
		render.OrDash(strings.Join(a.Name.locales(), ", ")), render.OrDash(strings.Join(a.Description.locales(), ", ")))
	if cats := categoryList(a.Categories); len(cats) > 0 {
		fmt.Fprintf(&b, "Categories: %s\n", strings.Join(cats, ", "))
	}
	if len(a.Tags) > 0 {
		fmt.Fprintf(&b, "Tags: %s\n", strings.Join(a.Tags, ", "))
	}
	for _, l := range []struct{ k, v string }{
		{"Homepage", linkURL(a.Homepage, loc)},
		{"Support URL", linkURL(a.SupportURL, loc)},
		{"Support email", a.SupportEmail.pick(loc)},
		{"Contributions", linkURL(a.ContributionsURL, loc)},
	} {
		if l.v != "" {
			fmt.Fprintf(&b, "%s: %s\n", l.k, l.v)
		}
	}
	if len(a.Authors) > 0 {
		names := make([]string, 0, len(a.Authors))
		for _, au := range a.Authors {
			names = append(names, firstNonEmpty(au.Name, au.Username))
		}
		fmt.Fprintf(&b, "Authors: %s\n", strings.Join(names, ", "))
	}
	if len(a.Promoted) > 0 {
		var ps []string
		for _, p := range a.Promoted {
			ps = append(ps, p.Category+" ("+strings.Join(p.Apps, ", ")+")")
		}
		fmt.Fprintf(&b, "Promoted: %s\n", strings.Join(ps, ", "))
	}
	fmt.Fprintf(&b, "Users: %d daily · %d downloads last week\n", a.AverageDailyUsers, a.WeeklyDownloads)
	fmt.Fprintf(&b, "Ratings: %.2f★ (bayesian %.2f) from %d, %d with text", a.Ratings.Average, a.Ratings.BayesianAverage, a.Ratings.Count, a.Ratings.TextCount)
	if g := a.Ratings.GroupedCounts; len(g) > 0 {
		fmt.Fprintf(&b, " · 5★ %d · 4★ %d · 3★ %d · 2★ %d · 1★ %d", g["5"], g["4"], g["3"], g["2"], g["1"])
	}
	b.WriteString("\n")
	for _, v := range []struct {
		label string
		v     *addonVersion
	}{{"Current version", a.CurrentVersion}, {"Latest unlisted", a.LatestUnlistedVersion}} {
		if v.v != nil {
			fmt.Fprintf(&b, "%s: %s (id %d, file %s, %s)\n", v.label, v.v.Version, v.v.ID, fileStatus(v.v), compat(v.v.Compatibility))
		}
	}
	fmt.Fprintf(&b, "Created %s · last updated %s\n", date(a.Created), date(a.LastUpdated))
	fmt.Fprintf(&b, "EULA: %t · privacy policy: %t\n", a.HasEULA, a.HasPrivacyPolicy)
	if len(a.Previews) > 0 {
		b.WriteString("Screenshots:\n")
		for _, p := range a.Previews {
			fmt.Fprintf(&b, "- preview %d (position %d) %s %s\n", p.ID, p.Position, render.MDCell(p.Caption.pick(loc)), p.ImageURL)
		}
	}
	fmt.Fprintf(&b, "Page: %s\n", a.URL)
	if a.EditURL != "" {
		fmt.Fprintf(&b, "Edit: %s\n", a.EditURL)
	}
	return b.String()
}

func renderUpload(up upload) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Upload %s (%s channel, version %s): ", up.UUID, render.OrDash(up.Channel), render.OrDash(up.Version))
	switch {
	case !up.Processed:
		b.WriteString("still being validated.\n")
		return b.String()
	case up.Valid && up.Submitted:
		b.WriteString("valid, already submitted.\n")
	case up.Valid:
		b.WriteString("VALID, ready to submit.\n")
	default:
		b.WriteString("INVALID.\n")
	}
	v := up.Validation
	if v == nil {
		return b.String()
	}
	fmt.Fprintf(&b, "%d error(s), %d warning(s), %d notice(s)\n", v.Errors, v.Warnings, v.Notices)
	const maxMessages = 40
	shown := 0
	for _, typ := range []string{"error", "warning", "notice"} {
		for _, m := range v.Messages {
			if m.Type != typ {
				continue
			}
			if shown == maxMessages {
				fmt.Fprintf(&b, "… %d more message(s)\n", len(v.Messages)-shown)
				return b.String()
			}
			shown++
			fmt.Fprintf(&b, "- %s: %s", strings.ToUpper(m.Type), stripTags(m.Message))
			if m.File != "" {
				fmt.Fprintf(&b, " [%s", m.File)
				if m.Line != nil {
					fmt.Fprintf(&b, ":%d", *m.Line)
				}
				b.WriteString("]")
			}
			if d := descriptionText(m.Description); d != "" && m.Type != "notice" {
				fmt.Fprintf(&b, " — %s", d)
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

func descriptionText(d any) string {
	switch x := d.(type) {
	case string:
		return stripTags(x)
	case []any:
		var parts []string
		for _, e := range x {
			if s, ok := e.(string); ok {
				parts = append(parts, stripTags(s))
			}
		}
		return strings.Join(parts, " ")
	}
	return ""
}

// stripTags removes simple HTML markup from validator messages.
func stripTags(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == '<':
			in = true
		case r == '>' && in:
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func renderUploadList(out page[upload]) string {
	if len(out.Results) == 0 {
		return "No uploads."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d upload(s), latest first:\n\n| uuid | version | channel | processed | valid | submitted | errors | warnings |\n|---|---|---|---|---|---|---:|---:|\n", out.Count)
	for _, u := range out.Results {
		errs, warns := 0, 0
		if u.Validation != nil {
			errs, warns = u.Validation.Errors, u.Validation.Warnings
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %t | %t | %t | %d | %d |\n", u.UUID, render.MDCell(u.Version), u.Channel, u.Processed, u.Valid, u.Submitted, errs, warns)
	}
	return b.String()
}

func renderVersionList(ref string, out page[addonVersion], pg int) string {
	if len(out.Results) == 0 {
		return "No versions match."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %d version(s)\n\n| id | version | channel | file | disabled | reviewed | compatibility | size |\n|---:|---|---|---|---|---|---|---:|\n", ref, out.Count)
	for _, v := range out.Results {
		reviewed, size := "-", "-"
		if v.Reviewed != nil {
			reviewed = date(*v.Reviewed)
		}
		if v.File != nil {
			size = humanBytes(int(v.File.Size))
		}
		fmt.Fprintf(&b, "| %d | %s | %s | %s | %t | %s | %s | %s |\n", v.ID, render.MDCell(v.Version), render.OrDash(v.Channel), fileStatus(&v),
			v.IsDisabled, reviewed, render.MDCell(compat(v.Compatibility)), size)
	}
	b.WriteString(nextPage(out.Next, pg))
	return b.String()
}

func renderVersion(v addonVersion) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Version %s (id %d) · %s channel · file %s", v.Version, v.ID, render.OrDash(v.Channel), fileStatus(&v))
	if v.IsDisabled {
		b.WriteString(" · DISABLED by developer")
	}
	b.WriteString("\n")
	if v.Reviewed != nil {
		fmt.Fprintf(&b, "Reviewed: %s\n", date(*v.Reviewed))
	}
	fmt.Fprintf(&b, "Compatibility: %s\n", render.OrDash(compat(v.Compatibility)))
	if f := v.File; f != nil {
		fmt.Fprintf(&b, "File %d: %s, created %s, %s\nDownload: %s\n", f.ID, humanBytes(int(f.Size)), date(f.Created), f.Hash, f.URL)
		for _, p := range []struct {
			label string
			v     []string
		}{
			{"Permissions", f.Permissions}, {"Optional permissions", f.OptionalPermissions}, {"Host permissions", f.HostPermissions},
			{"Data collection", f.DataCollectionPermissions}, {"Optional data collection", f.OptionalDataCollectionPermissions},
		} {
			if len(p.v) > 0 {
				fmt.Fprintf(&b, "%s: %s\n", p.label, strings.Join(p.v, ", "))
			}
		}
	}
	if l := v.License; l != nil {
		name := l.Name.pick()
		if l.Slug != nil {
			name += " (" + *l.Slug + ")"
		}
		if l.IsCustom {
			name += " [custom]"
		}
		fmt.Fprintf(&b, "License: %s\n", render.OrDash(strings.TrimSpace(name)))
	}
	for _, l := range v.ReleaseNotes.locales() {
		fmt.Fprintf(&b, "Release notes [%s]: %s\n", l, *v.ReleaseNotes[l])
	}
	if v.ApprovalNotes != "" {
		fmt.Fprintf(&b, "Notes for reviewers: %s\n", v.ApprovalNotes)
	}
	if v.Source != nil && *v.Source != "" {
		fmt.Fprintf(&b, "Source code: %s\n", *v.Source)
	}
	if v.EditURL != "" {
		fmt.Fprintf(&b, "Edit: %s\n", v.EditURL)
	}
	return b.String()
}

func renderRatings(out page[rating], pg int) string {
	var b strings.Builder
	if g := out.GroupedRatings; len(g) > 0 {
		fmt.Fprintf(&b, "All ratings: 5★ %d · 4★ %d · 3★ %d · 2★ %d · 1★ %d\n\n", g["5"], g["4"], g["3"], g["2"], g["1"])
	}
	if len(out.Results) == 0 {
		return b.String() + "No ratings match."
	}
	fmt.Fprintf(&b, "%d rating(s) match:\n\n| id | date | stars | version | user | review | developer reply |\n|---:|---|---|---|---|---|---|\n", out.Count)
	for _, r := range out.Results {
		body, reply, ver := "", "", "-"
		if r.Body != nil {
			body = *r.Body
		}
		if r.Reply != nil && r.Reply.Body != nil {
			reply = *r.Reply.Body
		}
		if r.Version != nil {
			ver = r.Version.Version
		}
		fmt.Fprintf(&b, "| %d | %s | %d★ | %s | %s | %s | %s |\n", r.ID, date(r.Created), r.Score, render.MDCell(ver),
			render.MDCell(r.User.Name), render.MDCell(body), render.MDCell(reply))
	}
	b.WriteString(nextPage(out.Next, pg))
	return b.String()
}

func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
