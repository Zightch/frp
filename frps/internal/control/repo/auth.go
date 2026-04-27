package repo

import (
	"context"
	"encoding/hex"
)

func (r *SQLRepository) LoadGroupRuntimeByClientID(ctx context.Context, clientID [16]byte) (GroupRuntime, error) {
	return r.loadGroupRuntime(
		ctx,
		`
SELECT
	id,
	name,
	client_secret_hash,
	effective_ip,
	enabled,
	control_transport_security,
	updated_at
FROM proxy_groups
WHERE client_id = ?
`,
		hex.EncodeToString(clientID[:]),
	)
}
