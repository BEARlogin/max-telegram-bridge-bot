package main

import "testing"

func TestPostPackagesUseCurrentImportPrices(t *testing.T) {
	for _, tc := range []struct {
		posts  int
		amount uint64
	}{
		{posts: 100, amount: 49000},
		{posts: 500, amount: 149000},
		{posts: 1000, amount: 249000},
	} {
		got, ok := postPackage(tc.posts)
		if !ok || got != tc.amount {
			t.Fatalf("postPackage(%d)=(%d,%v), want (%d,true)",
				tc.posts, got, ok, tc.amount)
		}
	}
}

func TestPostPackageRejectsArbitraryAmounts(t *testing.T) {
	for _, posts := range []int{-1, 0, 1, 99, 101, 499, 501, 999, 1001} {
		if amount, ok := postPackage(posts); ok || amount != 0 {
			t.Fatalf("postPackage(%d)=(%d,%v), want (0,false)", posts, amount, ok)
		}
	}
}
