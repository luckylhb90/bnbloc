package roomlog

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// A room with more deliverables than the newest few: .room/artifacts.md
// lists every one (paging through the listing), details the newest
// MaxListedDeliverables only (their producers are the only versions read),
// and copies only those (docs/prd/2026-10-14-multi-member-rooms.md rule 7).
func TestArtifactsIndexListsEveryDeliverable(t *testing.T) {
	const total = 240
	page := func(from, to int, next string) string {
		var items []string
		for i := from; i < to; i++ {
			items = append(items, fmt.Sprintf(`{"artifactId":"art_%03d","title":"file-%03d.md","kind":"markdown","currentVersion":1,"createdBy":"maya","updatedAtMs":%d}`, i, i, total-i))
		}
		body := `{"artifacts":[` + strings.Join(items, ",") + `]`
		if next != "" {
			body += `,"nextCursor":"` + next + `"`
		}
		return body + "}"
	}
	ff := &fakeFetch{bodies: map[string]string{
		"/v1/rooms/r1/artifacts?limit=200":           page(0, 200, "c1"),
		"/v1/rooms/r1/artifacts?cursor=c1&limit=200": page(200, total, ""),
	}}
	for i := 0; i < MaxListedDeliverables; i++ {
		ff.bodies[fmt.Sprintf("/v1/rooms/r1/artifacts/art_%03d/versions/1", i)] = fmt.Sprintf(`{"version":{"artifactId":"art_%03d","version":1,"authorUserId":"maya"}}`, i)
	}
	list, err := ReadDeliverables(context.Background(), ff.fetch, "r1", &ProducerCache{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != total || list[0].Producer.Person != "maya" || list[total-1].Producer != (Producer{}) {
		t.Fatalf("listed %d; newest producer %+v, oldest %+v", len(list), list[0].Producer, list[total-1].Producer)
	}
	content := DeliverablesFile(list, nil, Producer{}, nil).Content
	if !strings.Contains(content, "file-000.md (markdown) v1, id art_000: uploaded by maya") || !strings.Contains(content, "- file-239.md (markdown) v1, id art_239\n") {
		t.Fatalf("artifacts.md does not list the newest in detail and the oldest by id:\n%s", content)
	}

	var written []string
	copies, _ := CopyDeliverables(context.Background(), ff.fetch, "r1", list, nil, func(_ context.Context, name string, _ []byte) error {
		written = append(written, name)
		return nil
	})
	if len(copies) > MaxListedDeliverables {
		t.Fatalf("copied %d deliverables; want at most the newest %d", len(copies), MaxListedDeliverables)
	}
}
