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

	result, err := s.store.QueryContext(
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

func (s *Service) CreateRatePolicyBinding(ctx context.Context, policyID int64, payload RatePolicyBindingRequest) (RatePolicyBindingView, error) {
	if payload.TunnelID <= 0 {
		return RatePolicyBindingView{}, &Error{Status: http.StatusBadRequest, Message: "tunnel_id is required"}
	}

	var (
		item    RatePolicyBindingView
		groupID int64
	)
	err := s.store.WithTxContext(ctx, nil, func(tx *storage.Tx) error {
		if _, err := s.loadRatePolicyByID(ctx, tx, policyID); err != nil {
			return err
		}
		tunnel, err := s.loadBindingTunnel(ctx, tx, payload.TunnelID)
		if err != nil {
			return err
		}
		if err := ensureBindableTunnel(tunnel); err != nil {
			return err
		}
		if err := ensureTunnelUnbound(ctx, tx, tunnel.ID); err != nil {
			return err
		}

		now := schemaTimestamp()
		result, err := tx.ExecContext(
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
			tunnel.ID,
			now,
			now,
		)
		if err != nil {
			return wrapUniqueConstraintError(err, "tunnel is already bound to a rate policy")
		}

		item, err = s.loadRatePolicyBindingByID(ctx, tx, result.LastInsertID)
		if err != nil {
			return err
		}
		groupID = tunnel.GroupID
		return nil
	})
	if err != nil {
		return RatePolicyBindingView{}, err
	}

	s.refreshGroups(groupID)
	return item, nil
}

func (s *Service) DeleteRatePolicyBinding(ctx context.Context, policyID, tunnelID int64) error {
	var groupID int64
	err := s.store.WithTxContext(ctx, nil, func(tx *storage.Tx) error {
		if _, err := s.loadRatePolicyByID(ctx, tx, policyID); err != nil {
			return err
		}

		row, err := tx.QueryOneContext(
			ctx,
			`
SELECT t.group_id
FROM rate_policy_bindings AS rpb
JOIN tunnels AS t ON t.id = rpb.tunnel_id
WHERE rpb.rate_policy_id = ? AND rpb.tunnel_id = ?
`,
			policyID,
			tunnelID,
		)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return &Error{Status: http.StatusNotFound, Message: "rate policy binding not found"}
			}
			return fmt.Errorf("load rate policy binding before delete: %w", err)
		}
		groupID, err = rowInt64(row, "group_id")
		if err != nil {
			return fmt.Errorf("decode binding group id: %w", err)
		}

		result, err := tx.ExecContext(
			ctx,
			`DELETE FROM rate_policy_bindings WHERE rate_policy_id = ? AND tunnel_id = ?`,
			policyID,
			tunnelID,
		)
		if err != nil {
			return fmt.Errorf("delete rate policy binding: %w", err)
		}
		if result.RowsAffected == 0 {
			return &Error{Status: http.StatusNotFound, Message: "rate policy binding not found"}
		}
		return nil
	})
	if err != nil {
		return err
	}

	s.refreshGroups(groupID)
	return nil
}

func (s *Service) loadRatePolicyBindingByID(ctx context.Context, conn storage.Conn, id int64) (RatePolicyBindingView, error) {
	row, err := conn.QueryOneContext(
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
WHERE rpb.id = ?
`,
		id,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RatePolicyBindingView{}, &Error{Status: http.StatusNotFound, Message: "rate policy binding not found"}
		}
		return RatePolicyBindingView{}, fmt.Errorf("load rate policy binding: %w", err)
	}

	item, err := decodeRatePolicyBindingRow(row)
	if err != nil {
		return RatePolicyBindingView{}, fmt.Errorf("decode rate policy binding: %w", err)
	}
	return item, nil
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

func ensureTunnelUnbound(ctx context.Context, conn storage.Conn, tunnelID int64) error {
	row, err := conn.QueryOneContext(
		ctx,
		`SELECT rate_policy_id FROM rate_policy_bindings WHERE tunnel_id = ?`,
		tunnelID,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("load existing rate policy binding: %w", err)
	}
	boundPolicyID, err := rowInt64(row, "rate_policy_id")
	if err != nil {
		return fmt.Errorf("decode existing bound rate policy id: %w", err)
	}
	if boundPolicyID > 0 {
		return &Error{Status: http.StatusConflict, Message: "tunnel is already bound to a rate policy"}
	}
	return nil
}
