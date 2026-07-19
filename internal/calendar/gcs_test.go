package calendar

import "testing"

func TestParseGSURI(t *testing.T) {
	b, o, err := ParseGSURI("gs://tradex-demo-state/calendar-state.json")
	if err != nil {
		t.Fatal(err)
	}
	if b != "tradex-demo-state" || o != "calendar-state.json" {
		t.Fatalf("got %s %s", b, o)
	}
	if _, _, err := ParseGSURI("not-gs"); err == nil {
		t.Fatal("expected error")
	}
}
