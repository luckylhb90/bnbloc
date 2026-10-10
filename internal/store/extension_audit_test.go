package store

import (
	"context"
	"testing"
)

func TestExtensionAuditAppendAndReadNewestFirst(t *testing.T) {
	s := NewMemoryExtensionAudit()
	ctx := context.Background()
	_ = s.AppendExtAudit(ctx, ExtAuditRecord{Kind: "core", Name: "a", Action: "install", AtMs: 100})
	_ = s.AppendExtAudit(ctx, ExtAuditRecord{Kind: "auth", Name: "b", Action: "enable", AtMs: 200})
	_ = s.AppendExtAudit(ctx, ExtAuditRecord{Kind: "core", Name: "a", Action: "uninstall", AtMs: 300})

	all, err := s.ExtAuditRecords(ctx, "", 0, 0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(all) != 3 || all[0].AtMs != 300 || all[2].AtMs != 100 {
		t.Fatalf("records = %+v; want 3 newest-first (300,200,100)", all)
	}

	core, _ := s.ExtAuditRecords(ctx, "core", 0, 0)
	if len(core) != 2 {
		t.Fatalf("kind filter = %+v; want 2 core records", core)
	}

	before, _ := s.ExtAuditRecords(ctx, "", 0, 300)
	if len(before) != 2 || before[0].AtMs != 200 {
		t.Fatalf("beforeAtMs cursor = %+v; want records older than 300", before)
	}
}
