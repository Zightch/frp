package system

import "testing"

func TestNormalizeListenIP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "ipv4", input: " 127.0.0.1 ", want: "127.0.0.1"},
		{name: "ipv6", input: " 2001:0DB8::1 ", want: "2001:db8::1"},
		{name: "any ipv4", input: "0.0.0.0", want: AnyIPv4},
		{name: "any ipv6", input: "::", want: AnyIPv6},
		{name: "empty", input: " ", wantErr: true},
		{name: "hostname", input: "localhost", wantErr: true},
		{name: "cidr", input: "127.0.0.1/24", wantErr: true},
		{name: "host port", input: "127.0.0.1:7000", wantErr: true},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := NormalizeListenIP(test.input)
			if test.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", test.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalize listen ip: %v", err)
			}
			if got != test.want {
				t.Fatalf("unexpected normalized ip: got %q want %q", got, test.want)
			}
		})
	}
}

func TestIsSpecialListenIP(t *testing.T) {
	t.Parallel()

	if !IsSpecialListenIP(AnyIPv4) {
		t.Fatalf("expected %q to be special", AnyIPv4)
	}
	if !IsSpecialListenIP(AnyIPv6) {
		t.Fatalf("expected %q to be special", AnyIPv6)
	}
	if IsSpecialListenIP("127.0.0.1") {
		t.Fatal("unexpected local ip treated as special")
	}
}
