package ports

import "testing"

func TestDetectSpecificConflictsIgnoresWildcardsAndDifferentProtocols(t *testing.T) {
	conflicts := DetectSpecificConflicts([]SpecificClaim{
		{OwnerID: 1, Protocol: "tcp", EffectiveIP: "127.0.0.1", PortStart: 20000, PortEnd: 20000},
		{OwnerID: 2, Protocol: "udp", EffectiveIP: "127.0.0.1", PortStart: 20000, PortEnd: 20000},
		{OwnerID: 3, Protocol: "tcp", EffectiveIP: "0.0.0.0", PortStart: 20000, PortEnd: 20000},
		{OwnerID: 4, Protocol: "tcp", EffectiveIP: "192.168.1.10", PortStart: 20000, PortEnd: 20000},
	})
	if len(conflicts) != 0 {
		t.Fatalf("expected no specific conflicts, got %#v", conflicts)
	}
}

func TestDetectSpecificConflictsFindsSingleAndRangeOverlap(t *testing.T) {
	conflicts := DetectSpecificConflicts([]SpecificClaim{
		{OwnerID: 1, Protocol: "tcp", EffectiveIP: "127.0.0.1", PortStart: 20000, PortEnd: 20000},
		{OwnerID: 2, Protocol: "tcp", EffectiveIP: "127.0.0.1", PortStart: 19999, PortEnd: 20001},
		{OwnerID: 3, Protocol: "tcp", EffectiveIP: "127.0.0.1", PortStart: 21000, PortEnd: 21005},
		{OwnerID: 4, Protocol: "tcp", EffectiveIP: "127.0.0.1", PortStart: 21003, PortEnd: 21008},
	})

	if len(conflicts) != 4 {
		t.Fatalf("expected 4 conflicted owners, got %#v", conflicts)
	}

	first := conflicts[1]
	if first.OtherOwnerID != 2 || first.ConflictStart != 20000 || first.ConflictEnd != 20000 {
		t.Fatalf("unexpected first conflict: %#v", first)
	}

	rangeConflict := conflicts[3]
	if rangeConflict.OtherOwnerID != 4 || rangeConflict.ConflictStart != 21003 || rangeConflict.ConflictEnd != 21005 {
		t.Fatalf("unexpected range conflict: %#v", rangeConflict)
	}
}
