package librarysearch

import (
	"fmt"
	"strings"
	"testing"
)

// library is a slice of a real organization's Library: English default content and Chinese
// experts the organization wrote.
func library() []Doc {
	return []Doc{
		{Key: "app-builder", Name: "app-builder", Label: "App builder", Tags: []string{"apps", "pages", "ai", "polls", "forms", "网页", "投票", "表单"}, Description: "Builds any page people open from a link (landing pages, assistants, polls, forms) with the app SDK; bnbloc publishes and shares it.", Offered: true},
		{Key: "sql-helper", Name: "sql-helper", Label: "SQL 与数据查询助手", Tags: []string{"sql", "data", "query"}, Description: "把业务问题翻译成正确、高效、可读的 SQL:先确认口径和表结构,再写查询、解释逻辑。"},
		{Key: "contract-reviewer", Name: "contract-reviewer", Label: "合同条款审查助手", Tags: []string{"contract", "legal", "review", "risk"}, Description: "站在我方立场逐条审查商务合同:识别风险条款、缺失条款,按风险分级并给出修改建议。"},
		{Key: "weekly-report", Name: "weekly-report-writer", Label: "周报助手", Tags: []string{"weekly report", "status"}, Description: "把零散的工作记录整理成结论先行的周报或月报。"},
		{Key: "meeting-minutes", Name: "meeting-minutes", Label: "会议纪要专家", Tags: []string{"meeting", "minutes", "action items"}, Description: "把会议录音转写、速记或聊天记录整理成清晰的会议纪要、决策和行动项。", Offered: true},
		{Key: "postmortem", Name: "incident-postmortem", Label: "故障复盘专家", Tags: []string{"incident", "postmortem", "sre"}, Description: "把故障过程整理成无责复盘报告:时间线、影响、根因、改进项。"},
		{Key: "support-reply", Name: "support-reply", Label: "客户沟通专家", Tags: []string{"customer support", "reply"}, Description: "起草客户支持回复和事件通报。", Offered: true},
	}
}

func keys(docs []Doc) []string {
	out := make([]string, len(docs))
	for i, d := range docs {
		out[i] = d.Key
	}
	return out
}

func first(docs []Doc) string {
	if len(docs) == 0 {
		return ""
	}
	return docs[0].Key
}

func TestRankPlacesRequestsInEitherLanguage(t *testing.T) {
	for _, c := range []struct{ request, want string }{
		{"build a landing page with an AI assistant", "app-builder"},
		{"help me write a SQL query to count users per day", "sql-helper"},
		{"我要审查这份合同里的风险条款", "contract-reviewer"},
		{"把这周的工作记录整理成周报", "weekly-report"},
		{"写一个SQL查询统计每天的活跃用户", "sql-helper"},
		{"we had an outage, write the postmortem", "postmortem"},
		{"做一个投票页面让大家选午餐", "app-builder"},
	} {
		got := (Lexical{}).Rank(c.request, library(), 5)
		if first(got) != c.want {
			t.Errorf("%q: first = %q, want %q (%v)", c.request, first(got), c.want, keys(got))
		}
	}
}

func TestRankKnowsWordFamilies(t *testing.T) {
	docs := []Doc{{Key: "tr", Name: "translator", Label: "Translation expert", Description: "Translation of documents."}, {Key: "other", Name: "other", Label: "Other", Description: "something"}}
	if got := (Lexical{}).Rank("please translate this paragraph", docs, 5); first(got) != "tr" {
		t.Errorf("translate must find translation: %v", keys(got))
	}
	reviewer, reviewing := Tokens("reviewer"), Tokens("reviewing")
	if strings.Join(reviewer, ",") != strings.Join(reviewing, ",") {
		t.Errorf("reviewer %v and reviewing %v are one word", reviewer, reviewing)
	}
}

func TestTokensHanPairsAndFiller(t *testing.T) {
	if got := Tokens("the 合同审查 of a"); strings.Join(got, ",") != "合同,同审,审查" {
		t.Errorf("tokens = %v", got)
	}
	if got := Tokens("a 的 I"); len(got) != 1 || got[0] != "的" {
		t.Errorf("a lone Han character is a word, filler is not: %v", got)
	}
	if got := Tokens("  ,, "); len(got) != 0 {
		t.Errorf("punctuation is nothing: %v", got)
	}
}

// What no word matches (a paraphrase) is the decision stage's to read: the shortlist also
// carries what an administrator put forward, so a candidate the request shares no word with
// still reaches it.
func TestRankCarriesOfferedContentForParaphrases(t *testing.T) {
	got := (Lexical{}).Rank("turn my call notes into something I can send around", library(), 10)
	set := map[string]bool{}
	for _, k := range keys(got) {
		set[k] = true
	}
	if !set["meeting-minutes"] {
		t.Errorf("an offered candidate with no shared word must still be shortlisted: %v", keys(got))
	}
	if set["sql-helper"] || set["contract-reviewer"] {
		t.Errorf("content nobody offered and no word matched stays out: %v", keys(got))
	}
}

func TestRankMatchesComeBeforeOfferedFillAndOfferedNeverCrowdThemOut(t *testing.T) {
	docs := []Doc{}
	for i := 0; i < 30; i++ {
		docs = append(docs, Doc{Key: fmt.Sprintf("m%d", i), Name: fmt.Sprintf("invoice-%d", i), Label: "Invoice helper", Description: "invoice reconciliation"})
	}
	for i := 0; i < 30; i++ {
		docs = append(docs, Doc{Key: fmt.Sprintf("o%d", i), Name: fmt.Sprintf("offered-%d", i), Label: "Offered thing", Description: "unrelated", Offered: true})
	}
	got := (Lexical{}).Rank("reconcile the invoice", docs, 40)
	if len(got) != 40 {
		t.Fatalf("shortlist = %d, want 40", len(got))
	}
	matches := 0
	for _, d := range got {
		if strings.HasPrefix(d.Key, "m") {
			matches++
		}
	}
	if matches != 30 || !strings.HasPrefix(got[0].Key, "m") || !strings.HasPrefix(got[29].Key, "m") {
		t.Errorf("all 30 matches first, then offered fill: matches=%d first=%s", matches, got[0].Key)
	}
	// With few matches, offered content fills at most half.
	got = (Lexical{}).Rank("reconcile the invoice", append(docs[:2:2], docs[30:]...), 10)
	offered := 0
	for _, d := range got {
		if strings.HasPrefix(d.Key, "o") {
			offered++
		}
	}
	if offered > 5 || len(got) != 2+offered {
		t.Errorf("offered fill at most half of the shortlist: %d of %d", offered, len(got))
	}
}

func TestRankBoundsAndEmptyInputs(t *testing.T) {
	big := make([]Doc, 0, 2000)
	for i := 0; i < 2000; i++ {
		big = append(big, Doc{Key: fmt.Sprint(i), Name: fmt.Sprintf("pack-%d", i), Label: "reviewer helper", Description: "review things"})
	}
	if got := (Lexical{}).Rank("review", big, 0); len(got) != DefaultLimit {
		t.Errorf("default limit = %d", len(got))
	}
	if got := (Lexical{}).Rank("review", big, 10000); len(got) != MaxLimit {
		t.Errorf("the limit is clamped to %d, got %d", MaxLimit, len(got))
	}
	if got := (Lexical{}).Rank("anything", nil, 5); len(got) != 0 {
		t.Errorf("no docs, no candidates: %v", got)
	}
	// An empty request matches nothing: only what was offered is shortlisted, and no more than
	// half of a shortlist (5 -> 2) of the 3 offered.
	got := (Lexical{}).Rank("", library(), 5)
	if len(got) != 2 {
		t.Errorf("an empty request shortlists offered content up to half the limit: %v", keys(got))
	}
	for _, d := range got {
		if !d.Offered {
			t.Errorf("%s was not offered", d.Key)
		}
	}
}

// The ranker is deterministic: equal scores keep the catalog's order.
func TestRankIsDeterministic(t *testing.T) {
	docs := []Doc{{Key: "a", Name: "x", Description: "review"}, {Key: "b", Name: "y", Description: "review"}, {Key: "c", Name: "z", Description: "review"}}
	for i := 0; i < 20; i++ {
		if got := keys((Lexical{}).Rank("review", docs, 5)); strings.Join(got, "") != "abc" {
			t.Fatalf("order = %v", got)
		}
	}
}
