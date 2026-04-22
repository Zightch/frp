package ports

import "testing"

func TestListenSpaceOverlapsMatrix(t *testing.T) {
	tests := []struct {
		name  string
		left  string
		right string
		want  bool
	}{
		{name: "any4-any4", left: "0.0.0.0", right: "0.0.0.0", want: true},
		{name: "any4-any6", left: "0.0.0.0", right: "::", want: true},
		{name: "any4-specific4", left: "0.0.0.0", right: "127.0.0.1", want: true},
		{name: "any4-specific6", left: "0.0.0.0", right: "::1", want: false},
		{name: "any6-any6", left: "::", right: "::", want: true},
		{name: "any6-specific6", left: "::", right: "::1", want: true},
		{name: "any6-specific4", left: "::", right: "127.0.0.1", want: false},
		{name: "specific4-same", left: "127.0.0.1", right: "127.0.0.1", want: true},
		{name: "specific4-different", left: "127.0.0.1", right: "127.0.0.2", want: false},
		{name: "specific6-same", left: "::1", right: "::1", want: true},
		{name: "specific6-different", left: "::1", right: "2001:db8::1", want: false},
		{name: "specific4-specific6", left: "127.0.0.1", right: "::1", want: false},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			leftKind, leftAddr, ok := classifyListenIP(test.left)
			if !ok {
				t.Fatalf("classify left failed: %q", test.left)
			}
			rightKind, rightAddr, ok := classifyListenIP(test.right)
			if !ok {
				t.Fatalf("classify right failed: %q", test.right)
			}
			got := listenSpaceOverlaps(leftKind, leftAddr, rightKind, rightAddr)
			if got != test.want {
				t.Fatalf("unexpected overlap result: left=%q right=%q got=%v want=%v", test.left, test.right, got, test.want)
			}
			gotReverse := listenSpaceOverlaps(rightKind, rightAddr, leftKind, leftAddr)
			if gotReverse != test.want {
				t.Fatalf("unexpected reverse overlap result: left=%q right=%q got=%v want=%v", test.right, test.left, gotReverse, test.want)
			}
		})
	}
}

func TestDetectConflictsWithWildcardAndRange(t *testing.T) {
	conflicts := DetectConflicts([]Claim{
		{OwnerID: 1, Protocol: "tcp", EffectiveIP: "0.0.0.0", PortStart: 20000, PortEnd: 20002},
		{OwnerID: 2, Protocol: "tcp", EffectiveIP: "::", PortStart: 20001, PortEnd: 20001},
		{OwnerID: 3, Protocol: "tcp", EffectiveIP: "127.0.0.1", PortStart: 20001, PortEnd: 20001},
		{OwnerID: 4, Protocol: "tcp", EffectiveIP: "::1", PortStart: 20001, PortEnd: 20001},
		{OwnerID: 5, Protocol: "udp", EffectiveIP: "0.0.0.0", PortStart: 20001, PortEnd: 20001},
	})

	if len(conflicts) != 4 {
		t.Fatalf("unexpected conflict count: %#v", conflicts)
	}

	if first := conflicts[1]; first.OtherOwnerID != 2 || first.ConflictStart != 20001 || first.ConflictEnd != 20001 {
		t.Fatalf("unexpected first conflict: %#v", first)
	}
	if second := conflicts[2]; second.OtherOwnerID != 1 || second.OwnerEffectiveIP != "::" || second.OtherEffectiveIP != "0.0.0.0" {
		t.Fatalf("unexpected second conflict: %#v", second)
	}
	if third := conflicts[3]; third.OtherOwnerID != 1 || third.OwnerEffectiveIP != "127.0.0.1" {
		t.Fatalf("unexpected third conflict: %#v", third)
	}
	if fourth := conflicts[4]; fourth.OtherOwnerID != 2 || fourth.OwnerEffectiveIP != "::1" {
		t.Fatalf("unexpected fourth conflict: %#v", fourth)
	}
	if _, ok := conflicts[5]; ok {
		t.Fatalf("unexpected udp conflict: %#v", conflicts[5])
	}
}
