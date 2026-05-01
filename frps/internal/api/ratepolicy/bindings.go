package ratepolicy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"

	"github.com/zightch/frp/frps/internal/storage"
)

func (s *Service) ListRatePolicyBindings(ctx context.Context, policyID int64) ([]RatePolicyBindingView, error) {
	if _, err := s.loadRatePolicyByID(ctx, s.store, policyID); err != nil {
		return nil, err
	}

	items, err := s.loadRatePolicyBindings(ctx, s.store, policyID)
	if err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Service) UpdateRatePolicyBindings(ctx context.Context, policyID int64, payload RatePolicyBindingsUpdateRequest) ([]RatePolicyBindingView, error) {
	targetTunnelIDs, err := normalizeBindingTunnelIDs(payload.TunnelIDs)
	if err != nil {
		return nil, err
	}

	affectedGroupIDs := make(map[int64]struct{})
	var items []RatePolicyBindingView
	err = s.store.WithTxContext(ctx, nil, func(tx *storage.Tx) error {
		if _, err := s.loadRatePolicyByID(ctx, tx, policyID); err != nil {
			return err
		}

		currentBindings, err := s.loadRatePolicyBindings(ctx, tx, policyID)
		if err != nil {
			return err
		}

		targetSet := make(map[int64]struct{}, len(targetTunnelIDs))
		for _, tunnelID := range targetTunnelIDs {
			targetSet[tunnelID] = struct{}{}

			tunnel, err := s.loadBindingTunnel(ctx, tx, tunnelID)
			if err != nil {
				return err
			}
			if err := ensureBindableTunnel(tunnel); err != nil {
				return err
			}

			boundPolicyID, err := loadBoundPolicyID(ctx, tx, tunnelID)
			if err != nil {
				return err
			}

			switch boundPolicyID {
			case 0:
				affectedGroupIDs[tunnel.GroupID] = struct{}{}
				now := schemaTimestamp()
				if _, err := tx.ExecContext(
					ctx,
					`
INSERT INTO rate_policy_bindings (
	rate_policy_id,
	tunnel_id,
	created_at,
	updated_at
) VALUES (?, ?, ?, ?)
`,
					policyID,
					tunnelID,
					now,
					now,
				); err != nil {
					return wrapUniqueConstraintError(err, "tunnel is already bound to a rate policy")
				}
			case policyID:
				// Keep the existing binding as-is.
			default:
				affectedGroupIDs[tunnel.GroupID] = struct{}{}
				if _, err := tx.ExecContext(
					ctx,
					`UPDATE rate_policy_bindings SET rate_policy_id = ?, updated_at = ? WHERE tunnel_id = ?`,
					policyID,
					schemaTimestamp(),
					tunnelID,
				); err != nil {
					return fmt.Errorf("migrate rate policy binding: %w", err)
				}
			}
		}

		for _, binding := range currentBindings {
			if _, keep := targetSet[binding.TunnelID]; keep {
				continue
			}
			affectedGroupIDs[binding.GroupID] = struct{}{}
			if _, err := tx.ExecContext(
				ctx,
				`DELETE FROM rate_policy_bindings WHERE rate_policy_id = ? AND tunnel_id = ?`,
				policyID,
				binding.TunnelID,
			); err != nil {
				return fmt.Errorf("delete rate policy binding: %w", err)
			}
		}

		items, err = s.loadRatePolicyBindings(ctx, tx, policyID)
		return err
	})
	if err != nil {
		return nil, err
	}

	groupIDs := make([]int64, 0, len(affectedGroupIDs))
	for groupID := range affectedGroupIDs {
		groupIDs = append(groupIDs, groupID)
	}
	s.refreshGroups(groupIDs...)
	return items, nil
}

func (s *Service) loadRatePolicyBindings(ctx context.Context, conn storage.Conn, policyID int64) ([]RatePolicyBindingView, error) {
	result, err := conn.QueryContext(
		ctx,
		`
SELECT
	rpb.id,
	rpb.rate_policy_id,
	rpb.tunnel_id,
	COALESCE(t.group_id, 0) AS group_id,
	COALESCE(g.name, '') AS group_name,
	COALESCE(t.name, '') AS tunnel_name,
	COALESCE(t.protocol, '') AS protocol,
	COALESCE(t.remote_type, '') AS remote_type,
	COALESCE(t.remote_start, 0) AS remote_start,
	COALESCE(t.remote_end, 0) AS remote_end,
	rpb.created_at,
	rpb.updated_at
FROM rate_policy_bindings AS rpb
LEFT JOIN tunnels AS t ON t.id = rpb.tunnel_id
LEFT JOIN proxy_groups AS g ON g.id = t.group_id
WHERE rpb.rate_policy_id = ?
ORDER BY rpb.id
`,
		policyID,
	)
	if err != nil {
		return nil, fmt.Errorf("list rate policy bindings: %w", err)
	}

	items := make([]RatePolicyBindingView, 0, len(result.Rows))
	for _, row := range result.Rows {
		item, err := decodeRatePolicyBindingRow(row)
		if err != nil {
			return nil, fmt.Errorf("decode rate policy binding: %w", err)
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *Service) loadBindingTunnel(ctx context.Context, conn storage.Conn, tunnelID int64) (bindingTunnel, error) {
	row, err := conn.QueryOneContext(
		ctx,
		`
SELECT
	t.id,
	t.group_id,
	COALESCE(g.name, '') AS group_name,
	t.name,
	t.protocol,
	t.remote_type,
	t.remote_start,
	t.remote_end
FROM tunnels AS t
LEFT JOIN proxy_groups AS g ON g.id = t.group_id
WHERE t.id = ?
`,
		tunnelID,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return bindingTunnel{}, &Error{Status: http.StatusNotFound, Message: "tunnel not found"}
		}
		return bindingTunnel{}, fmt.Errorf("load tunnel for rate policy binding: %w", err)
	}

	item := bindingTunnel{
		GroupName:  rowString(row, "group_name"),
		Name:       rowString(row, "name"),
		Protocol:   rowString(row, "protocol"),
		RemoteType: rowString(row, "remote_type"),
	}
	if item.ID, err = rowInt64(row, "id"); err != nil {
		return bindingTunnel{}, fmt.Errorf("decode tunnel id: %w", err)
	}
	if item.GroupID, err = rowInt64(row, "group_id"); err != nil {
		return bindingTunnel{}, fmt.Errorf("decode tunnel group id: %w", err)
	}
	if item.RemoteStart, err = rowInt64(row, "remote_start"); err != nil {
		return bindingTunnel{}, fmt.Errorf("decode tunnel remote_start: %w", err)
	}
	if item.RemoteEnd, err = rowInt64(row, "remote_end"); err != nil {
		return bindingTunnel{}, fmt.Errorf("decode tunnel remote_end: %w", err)
	}
	return item, nil
}

func ensureBindableTunnel(tunnel bindingTunnel) error {
	switch tunnel.Protocol {
	case "tcp", "udp":
	default:
		return &Error{Status: http.StatusBadRequest, Message: "tunnel must be single-port tcp or udp to bind a rate policy"}
	}
	if tunnel.RemoteType != "single" || tunnel.RemoteStart != tunnel.RemoteEnd {
		return &Error{Status: http.StatusBadRequest, Message: "tunnel must be single-port tcp or udp to bind a rate policy"}
	}
	return nil
}

func loadBoundPolicyID(ctx context.Context, conn storage.Conn, tunnelID int64) (int64, error) {
	row, err := conn.QueryOneContext(
		ctx,
		`SELECT rate_policy_id FROM rate_policy_bindings WHERE tunnel_id = ?`,
		tunnelID,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("load existing rate policy binding: %w", err)
	}

	boundPolicyID, err := rowInt64(row, "rate_policy_id")
	if err != nil {
		return 0, fmt.Errorf("decode existing bound rate policy id: %w", err)
	}
	return boundPolicyID, nil
}

func normalizeBindingTunnelIDs(values []int64) ([]int64, error) {
	if len(values) == 0 {
		return nil, nil
	}

	seen := make(map[int64]struct{}, len(values))
	normalized := make([]int64, 0, len(values))
	for _, value := range values {
		if value <= 0 {
			return nil, &Error{Status: http.StatusBadRequest, Message: "tunnel_ids must only contain positive ids"}
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	return normalized, nil
}
