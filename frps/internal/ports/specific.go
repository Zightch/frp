package ports

import "github.com/zightch/frp/frps/internal/system"

type SpecificClaim struct {
	OwnerID     int64
	Protocol    string
	EffectiveIP string
	PortStart   int64
	PortEnd     int64
}

type SpecificConflict struct {
	OwnerID       int64
	OtherOwnerID  int64
	Protocol      string
	EffectiveIP   string
	ConflictStart int64
	ConflictEnd   int64
}

func DetectSpecificConflicts(claims []SpecificClaim) map[int64]SpecificConflict {
	conflicts := make(map[int64]SpecificConflict)
	for left := 0; left < len(claims); left++ {
		for right := left + 1; right < len(claims); right++ {
			conflict, ok := specificConflictBetween(claims[left], claims[right])
			if !ok {
				continue
			}
			if _, exists := conflicts[claims[left].OwnerID]; !exists {
				conflicts[claims[left].OwnerID] = conflict
			}
			if _, exists := conflicts[claims[right].OwnerID]; !exists {
				conflicts[claims[right].OwnerID] = SpecificConflict{
					OwnerID:       claims[right].OwnerID,
					OtherOwnerID:  claims[left].OwnerID,
					Protocol:      conflict.Protocol,
					EffectiveIP:   conflict.EffectiveIP,
					ConflictStart: conflict.ConflictStart,
					ConflictEnd:   conflict.ConflictEnd,
				}
			}
		}
	}
	return conflicts
}

func IsSpecificListenIP(addr string) bool {
	return addr != "" && !system.IsSpecialListenIP(addr)
}

func specificConflictBetween(left, right SpecificClaim) (SpecificConflict, bool) {
	if left.OwnerID == right.OwnerID {
		return SpecificConflict{}, false
	}
	if left.Protocol != right.Protocol {
		return SpecificConflict{}, false
	}
	if !IsSpecificListenIP(left.EffectiveIP) || !IsSpecificListenIP(right.EffectiveIP) {
		return SpecificConflict{}, false
	}
	if left.EffectiveIP != right.EffectiveIP {
		return SpecificConflict{}, false
	}

	conflictStart := maxInt64(left.PortStart, right.PortStart)
	conflictEnd := minInt64(left.PortEnd, right.PortEnd)
	if conflictStart > conflictEnd {
		return SpecificConflict{}, false
	}

	return SpecificConflict{
		OwnerID:       left.OwnerID,
		OtherOwnerID:  right.OwnerID,
		Protocol:      left.Protocol,
		EffectiveIP:   left.EffectiveIP,
		ConflictStart: conflictStart,
		ConflictEnd:   conflictEnd,
	}, true
}

func minInt64(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
