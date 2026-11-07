// Copyright (c) 2026 Gembit Soultan Shirazi
//
// This file is part of mimir-server, a server that trains, serves and manages mimir models,
// created and owned by Gembit Soultan Shirazi.

package templates

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/schema"
	"github.com/gsoultan/mimir-server/internal/training"
)

// Words that say what a column holds, by its name.
var (
	whoName      = regexp.MustCompile(`customer|client|account|buyer|member|user|shop|store|outlet|company|lead`)
	itemName     = regexp.MustCompile(`product|item|sku|article|service|menu|part|material`)
	latName      = regexp.MustCompile(`^lat$|latitude|^lat_|_lat$`)
	lonName      = regexp.MustCompile(`^lon$|^lng$|longitude|^lon_|^lng_|_lon$|_lng$`)
	categoryName = regexp.MustCompile(`categor|segment|type|class|industry|tier|channel|group|kind`)
	dateName     = regexp.MustCompile(`date|^day$|^week$|^month$|period|^time$|timestamp`)
	amountName   = regexp.MustCompile(`quantity|qty|units|amount|revenue|sales|total|value|volume|orders`)
	actionName   = regexp.MustCompile(`action|step|offer|treatment|campaign|channel|activity`)
	outcomeName  = regexp.MustCompile(`converted|won|success|outcome|result|accepted|bought|reward|revenue`)
	durationName = regexp.MustCompile(`days|duration|tenure|weeks|months|time_to|age`)
)

// maxIdeas bounds the "it can also answer" notes of a check.
const maxIdeas = 4

// suggest adds notes on what else the table can answer, which templates
// fit its columns, and which column would let it answer more, so people
// know what to expect from their data before they teach anything.
func suggest(c check, t *Template, tab *training.Table) {
	header := tab.Header
	lower := make([]string, len(header))
	for i, h := range header {
		lower[i] = strings.ToLower(h)
	}
	kind := map[string]schema.Kind{}
	for _, col := range tab.Columns {
		kind[col.Name] = col.Kind
	}
	find := func(re *regexp.Regexp, ok func(name string) bool) string {
		for i, h := range lower {
			if re.MatchString(h) && (ok == nil || ok(header[i])) {
				return header[i]
			}
		}
		return ""
	}
	number := func(name string) bool { return kind[name] == schema.Number || name == tab.Target && tab.Numeric }
	words := func(name string) bool { return !number(name) }
	who := find(whoName, words)
	item := find(itemName, func(n string) bool { return words(n) && n != who })
	lat, lon := find(latName, number), find(lonName, number)
	category := find(categoryName, func(n string) bool { return words(n) && n != who && n != item })
	date := find(dateName, nil)
	amount := find(amountName, func(n string) bool { return number(n) && n != lat && n != lon })
	action := find(actionName, func(n string) bool { return words(n) && n != who && n != item && n != category })
	outcome := find(outcomeName, func(n string) bool { return n != action })
	duration := find(durationName, func(n string) bool { return number(n) && n != date })

	title := func(name string) string {
		if a, ok := arch.Lookup(name); ok {
			return a.Title
		}
		return name
	}
	var ideas, unlock []string
	idea := func(s string) { ideas = append(ideas, s) }

	// What the answer column itself gives.
	switch {
	case t != nil:
	case tab.Classes != nil && len(tab.Classes) == 2:
		yes, share := yesShare(tab)
		idea(fmt.Sprintf("How likely %s is %s for a new row, from 0 to 100%%. %d%% of these rows say %s, so a model that does better than that has learned something.", tab.Target, yes, share, yes))
	case tab.Numeric:
		idea(fmt.Sprintf("A number for %s on a new row, with how far off it usually is. Pick %s.", tab.Target, title("number-auto")))
	case tab.Classes != nil && len(tab.Classes) <= schema.MaxValues:
		idea(fmt.Sprintf("Which of the %d values of %s fits a new row, with how likely each is.", len(tab.Classes), tab.Target))
	}
	if who != "" && item != "" {
		idea(fmt.Sprintf("The 10 %s each %s is likeliest to want next. Pick %s and make %s the answer.", plural2(item), readable(who), title("recommend"), item))
		switch {
		case lat != "" && lon != "":
			extra := ""
			if category != "" {
				extra = fmt.Sprintf(" of the same %s", readable(category))
			}
			idea(fmt.Sprintf("What %s%s within a distance you choose buy, for a %s or a new lead. Pick %s.", plural2(who), extra, readable(who), title("recommend-nearby")))
			if category == "" {
				unlock = append(unlock, fmt.Sprintf("a category column, like customer_category, so nearby recommendations count only %s of the same kind", plural2(who)))
			}
		default:
			unlock = append(unlock, fmt.Sprintf("latitude and longitude of each %s, to recommend what %s of the same kind within N km buy", readable(who), plural2(who)))
		}
	}
	if date != "" && amount != "" {
		by := ""
		if who != "" {
			by = " for each " + readable(who)
		}
		idea(fmt.Sprintf("How much %s comes next, day by day or week by week%s. Pick %s and make %s the answer.", amount, by, title("forecast"), amount))
	}
	if action != "" && outcome != "" {
		idea(fmt.Sprintf("Which %s works best in each situation, with the %s each would likely bring. Pick %s and make %s the answer.", action, outcome, title("best-action"), action))
	}
	if duration != "" && tab.Classes != nil && len(tab.Classes) == 2 {
		if _, ok := arch.Lookup("time-to-event"); ok {
			idea(fmt.Sprintf("The chance %s happens within any number of days you ask, counting rows where it hasn't happened yet. Pick %s.", tab.Target, title("time-to-event")))
		}
	}
	if len(tab.Columns) >= 3 {
		idea(fmt.Sprintf("Groups of similar rows, like kinds of %s, named after what sets each apart. Pick %s; it needs no answer column.", cmp.Or(plural2(who), "rows"), title("find-groups")))
	}
	for _, s := range ideas[:min(maxIdeas, len(ideas))] {
		c.note(info, "This data can answer:", s)
	}
	if len(unlock) > 0 {
		c.note(info, "Add a column to answer more:", strings.Join(unlock, "; ")+".")
	}

	// Templates whose columns are mostly here.
	if t == nil {
		type fit struct {
			t     Template
			share float64
		}
		var fits []fit
		for _, tp := range catalog.Templates {
			if tp.Data != Table && tp.Data != Text || len(tp.Columns) == 0 {
				continue
			}
			m := tp.Match(header, nil)
			if m[tp.Answer().Name] == "" {
				continue
			}
			if share := float64(len(m)) / float64(len(tp.Columns)); share >= 0.6 {
				fits = append(fits, fit{tp, share})
			}
		}
		slices.SortStableFunc(fits, func(a, b fit) int { return cmp.Compare(b.share, a.share) })
		var names []string
		for _, f := range fits[:min(3, len(fits))] {
			names = append(names, fmt.Sprintf("%s (%d%% of its columns)", f.t.Name, int(f.share*100+0.5)))
		}
		if len(names) > 0 {
			c.note(info, "Templates that fit these columns:", strings.Join(names, ", ")+". A template checks your data against what it needs and explains each column.")
		}
	}
}

// yesShare returns the answer that means yes and the percentage of rows
// that give it.
func yesShare(tab *training.Table) (string, int) {
	yes := tab.Classes[1]
	for _, v := range tab.Classes {
		if b, ok := yesNo(v); ok && b {
			yes = v
		}
	}
	total := 0
	for _, n := range tab.Answers {
		total += n
	}
	return yes, int(float64(tab.Answers[yes])*100/float64(max(total, 1)) + 0.5)
}

// readable makes a column name read as words: customer_id becomes
// customer.
func readable(name string) string {
	n := strings.ToLower(name)
	for _, s := range []string{"_id", "_code", "_name", " id"} {
		if strings.HasSuffix(n, s) && len(n) > len(s) {
			n = strings.TrimSuffix(n, s)
			break
		}
	}
	return strings.ReplaceAll(n, "_", " ")
}

// plural2 makes a column name read as many: customer_id becomes
// customers.
func plural2(name string) string {
	if name == "" {
		return ""
	}
	n := readable(name)
	switch {
	case strings.HasSuffix(n, "s"):
		return n
	case strings.HasSuffix(n, "y") && !strings.HasSuffix(n, "ey"):
		return strings.TrimSuffix(n, "y") + "ies"
	}
	return n + "s"
}
