package ports

import (
	"net"
	"strings"

	"github.com/zightch/frp/frps/internal/system"
)

type Claim struct {
	OwnerID     int64
	Protocol    string
	EffectiveIP string
	PortStart   int64
	PortEnd     int64
}

type Conflict struct {
	OwnerID          int64
	OtherOwnerID     int64
	Protocol         string
	OwnerEffectiveIP string
	OtherEffectiveIP string
	ConflictStart    int64
	ConflictEnd      int64
}

type listenIPKind uint8

const (
	listenIPUnknown listenIPKind = iota
	listenIPAny4
	listenIPAny6
	listenIPSpecific4
	listenIPSpecific6
)

func DetectConflicts(claims []Claim) map[int64]Conflict {
	conflicts := make(map[int64]Conflict)
	for left := 0; left < len(claims); left++ {
		for right := left + 1; right < len(claims); right++ {
			conflict, ok := conflictBetween(claims[left], claims[right])
			if !ok {
				continue
			}
			if _, exists := conflicts[claims[left].OwnerID]; !exists {
				conflicts[claims[left].OwnerID] = conflict
			}
			if _, exists := conflicts[claims[right].OwnerID]; !exists {
				conflicts[claims[right].OwnerID] = Conflict{
					OwnerID:          claims[right].OwnerID,
					OtherOwnerID:     claims[left].OwnerID,
					Protocol:         conflict.Protocol,
					OwnerEffectiveIP: conflict.OtherEffectiveIP,
					OtherEffectiveIP: conflict.OwnerEffectiveIP,
					ConflictStart:    conflict.ConflictStart,
					ConflictEnd:      conflict.ConflictEnd,
				}
			}
		}
	}
	return conflicts
}

func conflictBetween(left, right Claim) (Conflict, bool) {
	if left.OwnerID == right.OwnerID {
		return Conflict{}, false
	}
	if strings.TrimSpace(left.Protocol) != strings.TrimSpace(right.Protocol) {
		return Conflict{}, false
	}

	conflictStart := maxInt64(left.PortStart, right.PortStart)
	conflictEnd := minInt64(left.PortEnd, right.PortEnd)
	if conflictStart > conflictEnd {
		return Conflict{}, false
	}

	leftKind, leftAddr, ok := classifyListenIP(left.EffectiveIP)
	if !ok {
		return Conflict{}, false
	}
	rightKind, rightAddr, ok := classifyListenIP(right.EffectiveIP)
	if !ok {
		return Conflict{}, false
	}
	if !listenSpaceOverlaps(leftKind, leftAddr, rightKind, rightAddr) {
		return Conflict{}, false
	}

	return Conflict{
		OwnerID:          left.OwnerID,
		OtherOwnerID:     right.OwnerID,
		Protocol:         strings.TrimSpace(left.Protocol),
		OwnerEffectiveIP: leftAddr,
		OtherEffectiveIP: rightAddr,
		ConflictStart:    conflictStart,
		ConflictEnd:      conflictEnd,
	}, true
}

func classifyListenIP(raw string) (listenIPKind, string, bool) {
	normalized, err := system.NormalizeListenIP(raw)
	if err != nil {
		return listenIPUnknown, "", false
	}
	switch normalized {
	case system.AnyIPv4:
		return listenIPAny4, normalized, true
	case system.AnyIPv6:
		return listenIPAny6, normalized, true
	}

	parsed := net.ParseIP(normalized)
	if parsed == nil {
		return listenIPUnknown, "", false
	}
	if ipv4 := parsed.To4(); ipv4 != nil {
		return listenIPSpecific4, ipv4.String(), true
	}
	if ipv6 := parsed.To16(); ipv6 != nil {
		return listenIPSpecific6, ipv6.String(), true
	}
	return listenIPUnknown, "", false
}

func listenSpaceOverlaps(leftKind listenIPKind, leftAddr string, rightKind listenIPKind, rightAddr string) bool {
	switch leftKind {
	case listenIPAny4:
		return rightKind == listenIPAny4 || rightKind == listenIPAny6 || rightKind == listenIPSpecific4
	case listenIPAny6:
		return rightKind == listenIPAny4 || rightKind == listenIPAny6 || rightKind == listenIPSpecific6
	case listenIPSpecific4:
		if rightKind == listenIPAny4 {
			return true
		}
		if rightKind == listenIPSpecific4 {
			return leftAddr == rightAddr
		}
		return false
	case listenIPSpecific6:
		if rightKind == listenIPAny6 {
			return true
		}
		if rightKind == listenIPSpecific6 {
			return leftAddr == rightAddr
		}
		return false
	default:
		return false
	}
}
