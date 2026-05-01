package ratepolicy

import "testing"

func TestParseMode(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    Mode
		wantErr bool
	}{
		{name: "independent", input: "independent", want: ModeIndependent},
		{name: "shared", input: "shared", want: ModeShared},
		{name: "trimmed", input: "  shared  ", want: ModeShared},
		{name: "invalid", input: "burst", wantErr: true},
		{name: "empty", input: "", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseMode(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got mode %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse mode: %v", err)
			}
			if got != tc.want {
				t.Fatalf("unexpected mode: got %q want %q", got, tc.want)
			}
		})
	}
}

func TestParseUnitAndToBPS(t *testing.T) {
	unit, err := ParseUnit("")
	if err != nil {
		t.Fatalf("parse default unit: %v", err)
	}
	if unit != UnitM {
		t.Fatalf("unexpected default unit: %q", unit)
	}

	got, err := ToBPS(10, UnitM)
	if err != nil {
		t.Fatalf("convert Mbps: %v", err)
	}
	if got != 10_000_000 {
		t.Fatalf("unexpected bps: %d", got)
	}

	if _, err := ParseUnit("bps"); err == nil {
		t.Fatal("expected invalid unit to fail")
	}
	if _, err := ToBPS(0, UnitK); err == nil {
		t.Fatal("expected zero value to fail")
	}
}
