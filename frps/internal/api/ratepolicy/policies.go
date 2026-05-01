package ratepolicy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/zightch/frp/frps/internal/storage"
	policycore "github.com/zightch/frp/frps/pkg/ratepolicy"
)

func (s *Service) ListRatePolicies(ctx context.Context) ([]RatePolicyView, error) {
	result, err := s.store.QueryContext(
		ctx,
		`
SELECT
	rp.id,
	rp.name,
	rp.mode,
	rp.downlink_bps,
	rp.uplink_bps,
	rp.created_at,
	rp.updated_at,
	COUNT(rpb.id) AS binding_count
FROM rate_policies AS rp
LEFT JOIN rate_policy_bindings AS rpb ON rpb.rate_policy_id = rp.id
GROUP BY rp.id, rp.name, rp.mode, rp.downlink_bps, rp.uplink_bps, rp.created_at, rp.updated_at
ORDER BY rp.id
`,
	)
	if err != nil {
		return nil, fmt.Errorf("list rate policies: %w", err)
	}

	items := make([]RatePolicyView, 0, len(result.Rows))
	for _, row := range result.Rows {
		item, err := decodeRatePolicyRow(row)
		if err != nil {
			return nil, fmt.Errorf("decode rate policy: %w", err)
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *Service) CreateRatePolicy(ctx context.Context, payload RatePolicyRequest) (RatePolicyView, error) {
	normalized, err := normalizeRatePolicy(payload)
	if err != nil {
		return RatePolicyView{}, err
	}

	var item RatePolicyView
	err = s.store.WithTxContext(ctx, nil, func(tx *storage.Tx) error {
		now := schemaTimestamp()
		result, err := tx.ExecContext(
			ctx,
			`
INSERT INTO rate_policies (
	name,
	mode,
	downlink_bps,
	uplink_bps,
	created_at,
	updated_at
) VALUES (?, ?, ?, ?, ?, ?)
`,
			normalized.Name,
			normalized.Mode,
			normalized.DownlinkBPS,
			normalized.UplinkBPS,
			now,
			now,
		)
		if err != nil {
			return wrapUniqueConstraintError(err, "rate policy name already exists")
		}

		item, err = s.loadRatePolicyByID(ctx, tx, result.LastInsertID)
		return err
	})
	if err != nil {
		return RatePolicyView{}, err
	}

	return item, nil
}

func (s *Service) UpdateRatePolicy(ctx context.Context, id int64, payload RatePolicyRequest) (RatePolicyView, error) {
	normalized, err := normalizeRatePolicy(payload)
	if err != nil {
		return RatePolicyView{}, err
	}

	var (
		item     RatePolicyView
		groupIDs []int64
	)
	err = s.store.WithTxContext(ctx, nil, func(tx *storage.Tx) error {
		if _, err := s.loadRatePolicyByID(ctx, tx, id); err != nil {
			return err
		}

		result, err := tx.ExecContext(
			ctx,
			`
UPDATE rate_policies
SET name = ?, mode = ?, downlink_bps = ?, uplink_bps = ?, updated_at = ?
WHERE id = ?
`,
			normalized.Name,
			normalized.Mode,
			normalized.DownlinkBPS,
			normalized.UplinkBPS,
			schemaTimestamp(),
			id,
		)
		if err != nil {
			return wrapUniqueConstraintError(err, "rate policy name already exists")
		}
		if result.RowsAffected == 0 {
			return &Error{Status: http.StatusNotFound, Message: "rate policy not found"}
		}

		item, err = s.loadRatePolicyByID(ctx, tx, id)
		if err != nil {
			return err
		}
		groupIDs, err = s.loadAffectedGroupIDsByPolicyID(ctx, tx, id)
		return err
	})
	if err != nil {
		return RatePolicyView{}, err
	}

	s.refreshGroups(groupIDs...)
	return item, nil
}

func (s *Service) DeleteRatePolicy(ctx context.Context, id int64) error {
	return s.store.WithTxContext(ctx, nil, func(tx *storage.Tx) error {
		if _, err := s.loadRatePolicyByID(ctx, tx, id); err != nil {
			return err
		}
		count, err := s.countBindingsByPolicyID(ctx, tx, id)
		if err != nil {
			return err
		}
		if count > 0 {
			return &Error{Status: http.StatusConflict, Message: "rate policy still has bound tunnels"}
		}

		result, err := tx.ExecContext(ctx, "DELETE FROM rate_policies WHERE id = ?", id)
		if err != nil {
			return fmt.Errorf("delete rate policy: %w", err)
		}
		if result.RowsAffected == 0 {
			return &Error{Status: http.StatusNotFound, Message: "rate policy not found"}
		}
		return nil
	})
}

func (s *Service) loadRatePolicyByID(ctx context.Context, conn storage.Conn, id int64) (RatePolicyView, error) {
	row, err := conn.QueryOneContext(
		ctx,
		`
SELECT
	rp.id,
	rp.name,
	rp.mode,
	rp.downlink_bps,
	rp.uplink_bps,
	rp.created_at,
	rp.updated_at,
	COUNT(rpb.id) AS binding_count
FROM rate_policies AS rp
LEFT JOIN rate_policy_bindings AS rpb ON rpb.rate_policy_id = rp.id
WHERE rp.id = ?
GROUP BY rp.id, rp.name, rp.mode, rp.downlink_bps, rp.uplink_bps, rp.created_at, rp.updated_at
`,
		id,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RatePolicyView{}, &Error{Status: http.StatusNotFound, Message: "rate policy not found"}
		}
		return RatePolicyView{}, fmt.Errorf("load rate policy: %w", err)
	}

	item, err := decodeRatePolicyRow(row)
	if err != nil {
		return RatePolicyView{}, fmt.Errorf("decode rate policy: %w", err)
	}
	return item, nil
}

func (s *Service) countBindingsByPolicyID(ctx context.Context, conn storage.Conn, id int64) (int64, error) {
	row, err := conn.QueryOneContext(
		ctx,
		`SELECT COUNT(1) AS count FROM rate_policy_bindings WHERE rate_policy_id = ?`,
		id,
	)
	if err != nil {
		return 0, fmt.Errorf("count rate policy bindings: %w", err)
	}
	count, err := rowInt64(row, "count")
	if err != nil {
		return 0, fmt.Errorf("decode rate policy binding count: %w", err)
	}
	return count, nil
}

func (s *Service) loadAffectedGroupIDsByPolicyID(ctx context.Context, conn storage.Conn, id int64) ([]int64, error) {
	result, err := conn.QueryContext(
		ctx,
		`
SELECT DISTINCT t.group_id
FROM rate_policy_bindings AS rpb
JOIN tunnels AS t ON t.id = rpb.tunnel_id
WHERE rpb.rate_policy_id = ?
ORDER BY t.group_id
`,
		id,
	)
	if err != nil {
		return nil, fmt.Errorf("list affected proxy groups for rate policy: %w", err)
	}

	groupIDs := make([]int64, 0, len(result.Rows))
	for _, row := range result.Rows {
		groupID, err := rowInt64(row, "group_id")
		if err != nil {
			return nil, fmt.Errorf("decode affected proxy group id: %w", err)
		}
		groupIDs = append(groupIDs, groupID)
	}
	return groupIDs, nil
}

func normalizeRatePolicy(payload RatePolicyRequest) (normalizedRatePolicy, error) {
	name := strings.TrimSpace(payload.Name)
	if name == "" {
		return normalizedRatePolicy{}, &Error{Status: http.StatusBadRequest, Message: "rate policy name is required"}
	}

	mode, err := policycore.ParseMode(payload.Mode)
	if err != nil {
		return normalizedRatePolicy{}, &Error{Status: http.StatusBadRequest, Message: err.Error()}
	}
	downlinkBPS, err := normalizeRateValue(payload.Downlink)
	if err != nil {
		return normalizedRatePolicy{}, err
	}
	uplinkBPS, err := normalizeRateValue(payload.Uplink)
	if err != nil {
		return normalizedRatePolicy{}, err
	}

	return normalizedRatePolicy{
		Name:        name,
		Mode:        string(mode),
		DownlinkBPS: downlinkBPS,
		UplinkBPS:   uplinkBPS,
	}, nil
}

func normalizeRateValue(payload RateValueRequest) (int64, error) {
	unit, err := policycore.ParseUnit(payload.Unit)
	if err != nil {
		return 0, &Error{Status: http.StatusBadRequest, Message: err.Error()}
	}
	bps, err := policycore.ToBPS(payload.Value, unit)
	if err != nil {
		return 0, &Error{Status: http.StatusBadRequest, Message: err.Error()}
	}
	return bps, nil
}
