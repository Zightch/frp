package ratepolicy

import (
	"fmt"
	"math"
	"strings"
)

type Mode string

const (
	ModeIndependent Mode = "independent"
	ModeShared      Mode = "shared"
)

type Unit string

const (
	UnitK Unit = "K"
	UnitM Unit = "M"
	UnitG Unit = "G"
)

func NormalizeMode(value string) Mode {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(ModeShared):
		return ModeShared
	default:
		return ModeIndependent
	}
}

func ParseMode(value string) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(ModeIndependent):
		return ModeIndependent, nil
	case string(ModeShared):
		return ModeShared, nil
	default:
		return "", fmt.Errorf("mode must be independent or shared")
	}
}

func NormalizeUnit(value string) Unit {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case string(UnitK):
		return UnitK
	case string(UnitG):
		return UnitG
	default:
		return UnitM
	}
}

func ParseUnit(value string) (Unit, error) {
	unit := NormalizeUnit(value)
	switch trimmed := strings.ToUpper(strings.TrimSpace(value)); trimmed {
	case "", string(UnitK), string(UnitM), string(UnitG):
		return unit, nil
	default:
		return "", fmt.Errorf("rate unit must be K, M or G")
	}
}

func ToBPS(value int64, unit Unit) (int64, error) {
	if value <= 0 {
		return 0, fmt.Errorf("rate value must be greater than 0")
	}

	factor := int64(1_000_000)
	switch unit {
	case UnitK:
		factor = 1_000
	case UnitM:
		factor = 1_000_000
	case UnitG:
		factor = 1_000_000_000
	default:
		return 0, fmt.Errorf("rate unit must be K, M or G")
	}

	if value > math.MaxInt64/factor {
		return 0, fmt.Errorf("rate value is too large")
	}
	return value * factor, nil
}
