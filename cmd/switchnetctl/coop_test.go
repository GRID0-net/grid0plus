package main

import "testing"

func TestParseWeaponIDsRequiresExactlyFour(t *testing.T) {
	if _, err := parseWeaponIDs("20000,20010,20020"); err == nil {
		t.Error("expected an error for three weapon ids")
	}
	if _, err := parseWeaponIDs("20000,20010,20020,20030,20040"); err == nil {
		t.Error("expected an error for five weapon ids")
	}
	got, err := parseWeaponIDs("20000, 20010,20020, 20030")
	if err != nil {
		t.Fatalf("parseWeaponIDs: %v", err)
	}
	want := []int64{20000, 20010, 20020, 20030}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %d, want %d (order must be preserved: these become main_weapons positionally)", i, got[i], want[i])
		}
	}
}

func TestParseWeaponIDsRejectsGarbage(t *testing.T) {
	if _, err := parseWeaponIDs("20000,not-a-number,20020,20030"); err == nil {
		t.Error("expected an error for a non-numeric weapon id")
	}
}

func TestParseInt64ListAllowsEmpty(t *testing.T) {
	got, err := parseInt64List("")
	if err != nil {
		t.Fatalf("parseInt64List(\"\"): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want none", got)
	}
}

func TestParseInt64ListPreservesOrder(t *testing.T) {
	got, err := parseInt64List("100, 200,300")
	if err != nil {
		t.Fatalf("parseInt64List: %v", err)
	}
	want := []int64{100, 200, 300}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %d, want %d", i, got[i], want[i])
		}
	}
}
