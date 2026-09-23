package index

import (
	"context"
	"testing"

	"github.com/astralyx/lino/internal/ignore"
)

func TestTotalsAndLastReconcile(t *testing.T) {
	ctx := context.Background()
	root := newRoot(t)
	writeFile(t, root, "a.txt", "1\n2\n")
	writeFile(t, root, "b.txt", "x\n")
	writeFile(t, root, "c.bin", "a\x00b")
	db := mustOpen(t, root)
	defer db.Close()
	if _, ok, err := db.LastReconcile(ctx); ok || err != nil {
		t.Fatalf("fresh index has a reconcile record: %v", err)
	}
	rules, err := ignore.NewRules(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Reconcile(ctx, rules, 0); err != nil {
		t.Fatal(err)
	}
	tot, err := db.Totals(ctx)
	if err != nil || tot != (Totals{Files: 3, Lines: 3, Binary: 1}) {
		t.Fatalf("totals %+v %v", tot, err)
	}
	ri, ok, err := db.LastReconcile(ctx)
	if err != nil || !ok || ri.Files != 3 || ri.Added != 3 || ri.At.IsZero() {
		t.Fatalf("last reconcile %+v %v %v", ri, ok, err)
	}
}
