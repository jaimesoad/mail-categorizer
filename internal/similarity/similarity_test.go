package similarity

import (
	"context"
	"testing"

	"github.com/jaimesoad/mail-categorizer/internal/email"
)

func corpus() []Doc {
	return []Doc{
		{"a1", "bank", "Transaction completed. A transaction of $85.40 was completed on your account", 1},
		{"a2", "bank", "Transaction alert: your card transaction was approved at your bank account", 1},
		{"a3", "bank", "Estado de cuenta: tu transaccion fue completada en tu cuenta bancaria", 1},
		{"b1", "shop", "Your order has shipped. Thank you for your purchase, track the package", 1},
		{"b2", "shop", "Order confirmation: thank you for your purchase of the headphones", 1},
		{"c1", "work", "Meeting tomorrow: sprint planning agenda and project roadmap review", 1},
	}
}

func TestNeighborsRankBySimilarity(t *testing.T) {
	idx := Build(corpus())
	ns := idx.Neighbors("A transaction of $20 was completed on your bank account", 3, "")
	if len(ns) == 0 || ns[0].Category != "bank" {
		t.Fatalf("got %+v", ns)
	}
	for i := 1; i < len(ns); i++ {
		if ns[i].Sim > ns[i-1].Sim {
			t.Fatal("not sorted")
		}
	}
}

func TestNeighborsExcludeSelfAndDeterministic(t *testing.T) {
	idx := Build(corpus())
	for _, n := range idx.Neighbors(corpus()[0].Text, 5, "a1") {
		if n.ID == "a1" {
			t.Fatal("self not excluded")
		}
	}
	a := idx.Neighbors("thank you for your order", 5, "")
	b := Build(corpus()).Neighbors("thank you for your order", 5, "")
	for i := range a {
		if a[i] != b[i] {
			t.Fatal("non deterministic")
		}
	}
}

func TestEmptyAndUnrelated(t *testing.T) {
	if got := Build(nil).Neighbors("hello", 3, ""); got != nil {
		t.Fatal("empty index")
	}
	if got := Build(corpus()).Neighbors("zzzz qqqq", 3, ""); len(got) != 0 {
		t.Fatalf("unrelated: %+v", got)
	}
}

func TestSignalCappedBelowAutoThreshold(t *testing.T) {
	idx := Build(corpus())
	s := Signal{Load: func(context.Context) (Index, error) { return idx, nil }}
	ev, err := s.Evidence(context.Background(), email.Email{Subject: "Transaction completed", BodyText: "transaction of $10 completed on your bank account"}, nil)
	if err != nil || len(ev) == 0 {
		t.Fatalf("ev=%+v err=%v", ev, err)
	}
	best := ev[0]
	for _, e := range ev {
		if e.Weight > best.Weight {
			best = e
		}
		if e.Weight > 0.75 {
			t.Fatalf("exceeds cap: %v", e.Weight)
		}
	}
	if best.Category != "bank" {
		t.Fatalf("got %+v", ev)
	}
}
