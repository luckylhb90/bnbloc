package store

import (
	"context"
	"reflect"
	"testing"
)

// A component's value is read back by the component, scope and key that
// wrote it, never by another component; a scope lists its keys sorted; an
// uninstall removes every value of the component and a member's departure
// every component's values of that member.
func TestContractComponentValueStore(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		s, ctx := b.ComponentValues, context.Background()
		comp, other := uniq("service/conn-"), uniq("service/other-")
		maya, jack := "members/"+uniq("maya-"), "members/"+uniq("jack-")
		put := func(c, scope, key, v string) {
			t.Helper()
			if err := s.PutComponentValue(ctx, ComponentValue{Component: c, Scope: scope, Key: key, Sealed: v, UpdatedAtMs: 7}); err != nil {
				t.Fatal(err)
			}
		}
		put(comp, maya, "grant:wiki", "s1")
		put(comp, maya, "grant:issues", "s2")
		put(comp, maya, "grant:wiki", "s3")
		put(comp, jack, "grant:wiki", "j1")
		put(other, maya, "grant:wiki", "o1")
		if v, ok, err := s.GetComponentValue(ctx, comp, maya, "grant:wiki"); err != nil || !ok || v.Sealed != "s3" || v.UpdatedAtMs != 7 {
			t.Fatalf("get = %+v %v %v (the newest write)", v, ok, err)
		}
		if _, ok, _ := s.GetComponentValue(ctx, comp, maya, "nope"); ok {
			t.Fatal("an unknown key is found")
		}
		if keys, err := s.ListComponentValueKeys(ctx, comp, maya); err != nil || !reflect.DeepEqual(keys, []string{"grant:issues", "grant:wiki"}) {
			t.Fatalf("keys = %v %v", keys, err)
		}
		if err := s.DeleteComponentValue(ctx, comp, maya, "grant:issues"); err != nil {
			t.Fatal(err)
		}
		if keys, _ := s.ListComponentValueKeys(ctx, comp, maya); !reflect.DeepEqual(keys, []string{"grant:wiki"}) {
			t.Fatalf("after a delete keys = %v", keys)
		}
		if err := s.DeleteScopeValues(ctx, maya); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := s.GetComponentValue(ctx, other, maya, "grant:wiki"); ok {
			t.Fatal("a member's values survive their departure")
		}
		if _, ok, _ := s.GetComponentValue(ctx, comp, jack, "grant:wiki"); !ok {
			t.Fatal("another member's value went with maya's")
		}
		if err := s.DeleteComponentValues(ctx, comp); err != nil {
			t.Fatal(err)
		}
		if keys, _ := s.ListComponentValueKeys(ctx, comp, jack); len(keys) != 0 {
			t.Fatalf("an uninstalled component keeps %v", keys)
		}
	})
}
