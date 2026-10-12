package store

import (
	"context"
	"errors"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// AttachmentStore contract: rows are tenant- and room-scoped on both
// backends, and an id (global: the primary key carries no tenant) is
// written insert-only.
func TestContractAttachmentStore(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ctx, octx := tenantCtx(uniq("t-att-")), tenantCtx(uniq("t-att-other-"))
		s := b.Attachments
		room, id := uniq("room-"), uniq("att-")
		att := Attachment{ID: id, RoomID: room, Uploader: "maya", Filename: "notes.txt", ContentType: "text/plain", Size: 12,
			SHA256: "abc", BlobKey: "sha256/abc", CreatedAtMs: 7}
		if err := s.PutAttachment(ctx, att); err != nil {
			t.Fatal(err)
		}
		if got, ok := must2(s.GetAttachment(ctx, room, id))(t); !ok || got != att {
			t.Fatalf("GetAttachment = %+v %v, want %+v", got, ok, att)
		}
		for _, c := range []struct {
			name     string
			ctx      context.Context
			room, id string
		}{
			{"unknown id", ctx, room, uniq("nope-")},
			{"another room", ctx, uniq("room-"), id},
			{"another tenant", octx, room, id},
		} {
			if _, ok := must2(s.GetAttachment(c.ctx, c.room, c.id))(t); ok {
				t.Fatalf("GetAttachment for %s found a row", c.name)
			}
		}

		// Insert-only: the id is taken in every room and tenant, and the
		// first row stays.
		again := att
		again.Filename = "hijack.txt"
		if err := s.PutAttachment(ctx, again); !errors.Is(err, ErrAttachmentExists) {
			t.Fatalf("re-put = %v, want ErrAttachmentExists", err)
		}
		again.RoomID = uniq("room-")
		if err := s.PutAttachment(octx, again); !errors.Is(err, ErrAttachmentExists) {
			t.Fatalf("re-put from another tenant = %v, want ErrAttachmentExists", err)
		}
		if got, _ := must2(s.GetAttachment(ctx, room, id))(t); got.Filename != "notes.txt" {
			t.Fatalf("stored row after refused re-puts = %+v", got)
		}
		if _, _, err := s.GetAttachment(bg(), room, id); !errors.Is(err, tenant.ErrNoTenant) {
			t.Fatalf("GetAttachment without tenant = %v", err)
		}
		if err := s.PutAttachment(bg(), Attachment{ID: uniq("att-"), RoomID: room}); !errors.Is(err, tenant.ErrNoTenant) {
			t.Fatalf("PutAttachment without tenant = %v", err)
		}
	})
}
