// Package librarysearch finds the experts, skill packs and tools of a member's Library
// that may fit a request they sent (docs/prd/2026-10-25-suggest-what-to-use.md). It is
// the first stage of a suggestion: a bounded shortlist, so the second stage (the
// organization's decision model, one choice question) costs the same in a Library of
// forty and of forty thousand. The ranker is a seam: Rank is the lexical default (BM25
// over each component's name, label, tags and description, any language); an
// organization with a larger Library plugs a semantic ranker behind the same Ranker
// interface and nothing above it changes.
//
// What a request shares no word with ("is this agreement safe to sign" against "review
// contracts") is the decision model's to read, not a lexical ranker's, so a shortlist is
// the lexical matches AND the components an administrator put forward (organization and
// platform content), up to a bound.
package librarysearch

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

// Doc is what the ranker reads of one candidate: its identity, the fields a request can
// match, and whether an administrator put it forward.
type Doc struct {
	// Key identifies the candidate to its caller; the ranker only returns it.
	Key         string
	Name        string
	Label       string
	Tags        []string
	Description string
	// Offered marks content an administrator put forward (organization and platform
	// scope): it is always a candidate, ahead of equally ranked ones, up to the bound.
	Offered bool
}

// Ranker orders docs by how well they fit a request, best first, at most limit.
type Ranker interface {
	Rank(request string, docs []Doc, limit int) []Doc
}

// Default and bounds of a shortlist.
const (
	// DefaultLimit is how many candidates the decision stage is asked about: about
	// 2,500 input tokens, under two seconds.
	DefaultLimit = 60
	// MaxLimit is the most a caller may ask for (the decision API takes 255).
	MaxLimit = 100
	// offeredDivisor: offered components fill at most 1/offeredDivisor of a shortlist
	// when the lexical matches do not (a half).
	offeredDivisor = 2
)

// Limit clamps a requested shortlist size.
func Limit(n int) int {
	if n <= 0 {
		return DefaultLimit
	}
	return min(n, MaxLimit)
}

// Lexical is the default Ranker: BM25 with fields weighted name over tags over
// description.
type Lexical struct{}

// stems: a word is known by its first stemChars letters, so "translate" and
// "translation", "review" and "reviewer" are one word.
const stemChars = 6

var stop = map[string]bool{}

func init() {
	for _, w := range strings.Fields("a an the and or of to in on for with from by is are be this that it its as at we you your my our me i can how do what which who please into over then them they there here also just more some any all not no yes if so but out up down new one two") {
		stop[w] = true
	}
}

// Tokens are a text's searchable words: lower-cased, filler dropped, long words cut to
// their stem, a run of Han characters as overlapping pairs (no word boundaries).
func Tokens(text string) []string {
	var out []string
	var word []rune
	flush := func() {
		if len(word) == 0 {
			return
		}
		w := string(word)
		word = word[:0]
		if unicode.Is(unicode.Han, []rune(w)[0]) {
			rs := []rune(w)
			if len(rs) == 1 {
				out = append(out, w)
			}
			for i := 0; i+1 < len(rs); i++ {
				out = append(out, string(rs[i:i+2]))
			}
			return
		}
		if len([]rune(w)) < 2 || stop[w] {
			return
		}
		if rs := []rune(w); len(rs) > stemChars {
			w = string(rs[:stemChars])
		}
		out = append(out, w)
	}
	var han *bool
	for _, r := range strings.ToLower(text) {
		switch {
		case unicode.Is(unicode.Han, r):
			if han != nil && !*han {
				flush()
			}
			t := true
			han = &t
			word = append(word, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if han != nil && *han {
				flush()
			}
			f := false
			han = &f
			word = append(word, r)
		default:
			flush()
			han = nil
		}
	}
	flush()
	return out
}

// Field weights: a word in a name says more than in a description.
const (
	weightName = 3
	weightTags = 2
	weightDesc = 1
	k1         = 1.2
	b          = 0.75
)

type indexed struct {
	doc  Doc
	tf   map[string]float64
	size float64
}

// Rank orders docs by BM25 against the request: best first, at most limit, offered docs
// filling what the matches do not up to half of limit.
func (Lexical) Rank(request string, docs []Doc, limit int) []Doc {
	limit = Limit(limit)
	idx := make([]indexed, len(docs))
	df := map[string]int{}
	var total float64
	for i, d := range docs {
		x := indexed{doc: d, tf: map[string]float64{}}
		add := func(text string, w float64) {
			for _, t := range Tokens(text) {
				x.tf[t] += w
				x.size += w
			}
		}
		add(d.Name+" "+d.Label, weightName)
		add(strings.Join(d.Tags, " "), weightTags)
		add(d.Description, weightDesc)
		for t := range x.tf {
			df[t]++
		}
		total += x.size
		idx[i] = x
	}
	avg := total / math.Max(1, float64(len(docs)))
	query := map[string]bool{}
	for _, t := range Tokens(request) {
		query[t] = true
	}
	type scored struct {
		doc   Doc
		score float64
		order int
	}
	var matched, offered []scored
	for i, x := range idx {
		var s float64
		for t := range query {
			tf, ok := x.tf[t]
			if !ok {
				continue
			}
			idf := math.Log(1 + (float64(len(docs))-float64(df[t])+0.5)/(float64(df[t])+0.5))
			s += idf * tf * (k1 + 1) / (tf + k1*(1-b+b*x.size/math.Max(1, avg)))
		}
		switch {
		case s > 0:
			matched = append(matched, scored{x.doc, s, i})
		case x.doc.Offered:
			offered = append(offered, scored{x.doc, 0, i})
		}
	}
	sort.SliceStable(matched, func(i, j int) bool {
		if matched[i].score != matched[j].score {
			return matched[i].score > matched[j].score
		}
		return matched[i].order < matched[j].order
	})
	out := make([]Doc, 0, limit)
	for _, m := range matched {
		if len(out) == limit {
			return out
		}
		out = append(out, m.doc)
	}
	// What no word matched, an administrator's offered content may still fit (a
	// paraphrase): it fills the shortlist up to half, never crowding a match out.
	room := min(limit-len(out), limit/offeredDivisor)
	for _, o := range offered {
		if room == 0 {
			break
		}
		out = append(out, o.doc)
		room--
	}
	return out
}
