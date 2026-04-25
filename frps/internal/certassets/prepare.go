package certassets

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"
)

type PrepareOptions struct {
	Now                time.Time
	LoadSystemCertPool func() (*x509.CertPool, error)
}

type PreparedAsset struct {
	Asset        Asset
	Certificates []*x509.Certificate
	Leaf         *x509.Certificate
}

type Runtime struct {
	systemCAPool  *x509.CertPool
	systemCACount int
	assets        []PreparedAsset
	assetsByID    map[int64]PreparedAsset
}

func PrepareRuntime(ctx context.Context, repo Repository) (*Runtime, error) {
	return PrepareRuntimeWithOptions(ctx, repo, PrepareOptions{})
}

func PrepareRuntimeWithOptions(ctx context.Context, repo Repository, options PrepareOptions) (*Runtime, error) {
	if repo == nil {
		return nil, fmt.Errorf("certificate assets repository is nil")
	}

	assets, err := repo.ListAssets(ctx)
	if err != nil {
		return nil, err
	}

	prepared, systemCAPool, err := PrepareAssetsWithOptions(assets, options)
	if err != nil {
		return nil, err
	}

	runtime := &Runtime{
		systemCAPool:  systemCAPool,
		systemCACount: len(systemCAPool.Subjects()),
		assets:        make([]PreparedAsset, 0, len(prepared)),
		assetsByID:    make(map[int64]PreparedAsset, len(prepared)),
	}
	for _, asset := range prepared {
		runtime.assets = append(runtime.assets, asset)
		runtime.assetsByID[asset.Asset.ID] = asset
	}
	return runtime, nil
}

func PrepareAssetsWithOptions(assets []Asset, options PrepareOptions) ([]PreparedAsset, *x509.CertPool, error) {
	now := options.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}

	loadSystemCertPool := options.LoadSystemCertPool
	if loadSystemCertPool == nil {
		loadSystemCertPool = x509.SystemCertPool
	}

	systemCAPool, err := loadSystemCertPool()
	if err != nil {
		return nil, nil, fmt.Errorf("load system ca pool: %w", err)
	}
	if systemCAPool == nil {
		systemCAPool = x509.NewCertPool()
	}

	prepared, err := prepareAssets(assets, systemCAPool, now)
	if err != nil {
		return nil, nil, err
	}
	return prepared, systemCAPool, nil
}

func (r *Runtime) SystemCACount() int {
	if r == nil {
		return 0
	}
	return r.systemCACount
}

func (r *Runtime) AssetCount() int {
	if r == nil {
		return 0
	}
	return len(r.assets)
}

func (r *Runtime) Assets() []PreparedAsset {
	if r == nil || len(r.assets) == 0 {
		return nil
	}
	cloned := make([]PreparedAsset, len(r.assets))
	copy(cloned, r.assets)
	return cloned
}

func ComputeCRTHash(crt string) (string, error) {
	certs, err := parseCertificatesPEM(crt)
	if err != nil {
		return "", err
	}
	return computeCertificateHash(certs), nil
}

func prepareAssets(assets []Asset, systemCAPool *x509.CertPool, now time.Time) ([]PreparedAsset, error) {
	preparedByID := make(map[int64]PreparedAsset, len(assets))
	prepared := make([]PreparedAsset, 0, len(assets))
	parseErrors := make([]error, 0)

	for _, asset := range assets {
		current, err := parseAsset(asset, now)
		if err != nil {
			parseErrors = append(parseErrors, wrapAssetError(asset, err))
			continue
		}
		prepared = append(prepared, current)
		preparedByID[asset.ID] = current
	}
	if len(parseErrors) > 0 {
		return nil, errors.Join(parseErrors...)
	}

	relationshipErrors := make([]error, 0)
	for _, current := range prepared {
		if err := validateIssuerRelation(current, preparedByID); err != nil {
			relationshipErrors = append(relationshipErrors, wrapAssetError(current.Asset, err))
		}
	}
	if cycleErr := detectIssuerCycles(prepared); cycleErr != nil {
		relationshipErrors = append(relationshipErrors, cycleErr)
	}
	if len(relationshipErrors) > 0 {
		return nil, errors.Join(relationshipErrors...)
	}

	verificationErrors := make([]error, 0)
	for _, current := range prepared {
		if err := validateAssetChain(current, preparedByID, systemCAPool, now); err != nil {
			verificationErrors = append(verificationErrors, wrapAssetError(current.Asset, err))
		}
	}
	if len(verificationErrors) > 0 {
		return nil, errors.Join(verificationErrors...)
	}

	return prepared, nil
}

func parseAsset(asset Asset, now time.Time) (PreparedAsset, error) {
	if err := validateStaticAssetFields(asset); err != nil {
		return PreparedAsset{}, err
	}

	certs, err := parseCertificatesPEM(asset.CRT)
	if err != nil {
		return PreparedAsset{}, fmt.Errorf("parse crt pem: %w", err)
	}
	if len(certs) == 0 {
		return PreparedAsset{}, fmt.Errorf("crt must contain at least one certificate")
	}

	computedHash := computeCertificateHash(certs)
	if computedHash != strings.ToLower(strings.TrimSpace(asset.CRTHash)) {
		return PreparedAsset{}, fmt.Errorf("crt_hash mismatch")
	}

	if err := validateAssetSourceStructure(asset, certs); err != nil {
		return PreparedAsset{}, err
	}
	if err := validatePrivateKey(asset, now); err != nil {
		return PreparedAsset{}, err
	}
	if err := validateCertificateUsage(asset, certs[0]); err != nil {
		return PreparedAsset{}, err
	}
	if err := validateAssetTopology(asset, certs[0]); err != nil {
		return PreparedAsset{}, err
	}
	if err := validateCertificateTimes(certs, now); err != nil {
		return PreparedAsset{}, err
	}

	return PreparedAsset{
		Asset:        asset,
		Certificates: certs,
		Leaf:         certs[0],
	}, nil
}

func validateStaticAssetFields(asset Asset) error {
	if asset.ID <= 0 {
		return fmt.Errorf("id must be greater than zero")
	}
	if strings.TrimSpace(asset.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if strings.TrimSpace(asset.CRT) == "" {
		return fmt.Errorf("crt is required")
	}
	if strings.TrimSpace(asset.CRTHash) == "" {
		return fmt.Errorf("crt_hash is required")
	}

	switch asset.Source {
	case SourceUpload, SourceGenerated:
	default:
		return fmt.Errorf("unsupported source %q", asset.Source)
	}

	switch asset.AssetType {
	case AssetTypeCertificate, AssetTypeCA:
	default:
		return fmt.Errorf("unsupported asset_type %q", asset.AssetType)
	}

	if asset.FormatType != FormatTypePEM {
		return fmt.Errorf("unsupported format_type %q", asset.FormatType)
	}
	return nil
}

func validateAssetSourceStructure(asset Asset, certs []*x509.Certificate) error {
	switch asset.Source {
	case SourceGenerated:
		if len(certs) != 1 {
			return fmt.Errorf("generated asset must contain exactly one certificate")
		}
	case SourceUpload:
	}
	return nil
}

func validatePrivateKey(asset Asset, _ time.Time) error {
	hasKey := strings.TrimSpace(asset.Key) != ""
	if asset.AssetType == AssetTypeCertificate && !hasKey {
		return fmt.Errorf("certificate asset requires key")
	}
	if !hasKey {
		return nil
	}
	if _, err := tls.X509KeyPair([]byte(asset.CRT), []byte(asset.Key)); err != nil {
		return fmt.Errorf("parse key or certificate pair: %w", err)
	}
	return nil
}

func validateAssetTopology(asset Asset, leaf *x509.Certificate) error {
	if asset.Source == SourceUpload {
		if asset.HasIssuer() {
			return fmt.Errorf("uploaded asset cannot have issuer relation")
		}
		return nil
	}

	if asset.Source != SourceGenerated {
		return nil
	}
	if asset.HasIssuer() {
		return nil
	}
	if asset.AssetType == AssetTypeCA && isSelfSigned(leaf) {
		return nil
	}
	if asset.AssetType == AssetTypeCertificate {
		return fmt.Errorf("generated certificate requires issuer relation")
	}
	return fmt.Errorf("generated ca requires issuer relation unless self-signed")
}

func validateCertificateUsage(asset Asset, leaf *x509.Certificate) error {
	if leaf == nil {
		return fmt.Errorf("leaf certificate is missing")
	}

	switch asset.AssetType {
	case AssetTypeCA:
		if !leaf.IsCA || !leaf.BasicConstraintsValid {
			return fmt.Errorf("ca asset must contain a valid ca certificate")
		}
	case AssetTypeCertificate:
		if leaf.IsCA {
			return fmt.Errorf("certificate asset cannot contain a ca certificate")
		}
	}
	return nil
}

func validateCertificateTimes(certs []*x509.Certificate, now time.Time) error {
	for index, cert := range certs {
		if cert.NotAfter.Before(cert.NotBefore) {
			return fmt.Errorf("certificate[%d] has invalid time range", index)
		}
		if now.Before(cert.NotBefore) {
			return fmt.Errorf("certificate[%d] is not valid before %s", index, cert.NotBefore.UTC().Format(time.RFC3339))
		}
		if now.After(cert.NotAfter) {
			return fmt.Errorf("certificate[%d] expired at %s", index, cert.NotAfter.UTC().Format(time.RFC3339))
		}
	}
	return nil
}

func validateIssuerRelation(asset PreparedAsset, assetsByID map[int64]PreparedAsset) error {
	if !asset.Asset.HasIssuer() {
		return nil
	}

	if asset.Asset.Source != SourceGenerated {
		return fmt.Errorf("uploaded asset cannot have issuer relation")
	}

	issuerAssetID := *asset.Asset.IssuerAssetID
	if issuerAssetID == asset.Asset.ID {
		return fmt.Errorf("issuer_asset_id cannot reference itself")
	}

	issuer, ok := assetsByID[issuerAssetID]
	if !ok {
		return fmt.Errorf("issuer asset %d does not exist", issuerAssetID)
	}
	if issuer.Asset.Source != SourceGenerated {
		return fmt.Errorf("issuer asset %d must be a generated ca asset", issuerAssetID)
	}
	if issuer.Asset.AssetType != AssetTypeCA {
		return fmt.Errorf("issuer asset %d must be a ca asset", issuerAssetID)
	}
	if err := asset.Leaf.CheckSignatureFrom(issuer.Leaf); err != nil {
		return fmt.Errorf("issuer asset %d does not sign current certificate: %w", issuerAssetID, err)
	}
	return nil
}

func detectIssuerCycles(prepared []PreparedAsset) error {
	state := make(map[int64]int, len(prepared))
	assetsByID := make(map[int64]PreparedAsset, len(prepared))
	for _, asset := range prepared {
		assetsByID[asset.Asset.ID] = asset
	}

	var walk func(PreparedAsset) error
	walk = func(asset PreparedAsset) error {
		switch state[asset.Asset.ID] {
		case 1:
			return fmt.Errorf("issuer_asset_id cycle detected at asset %d", asset.Asset.ID)
		case 2:
			return nil
		}

		state[asset.Asset.ID] = 1
		if asset.Asset.HasIssuer() {
			if next, ok := assetsByID[*asset.Asset.IssuerAssetID]; ok {
				if err := walk(next); err != nil {
					return err
				}
			}
		}
		state[asset.Asset.ID] = 2
		return nil
	}

	for _, asset := range prepared {
		if err := walk(asset); err != nil {
			return err
		}
	}
	return nil
}

func validateAssetChain(asset PreparedAsset, assetsByID map[int64]PreparedAsset, systemCAPool *x509.CertPool, now time.Time) error {
	roots := x509.NewCertPool()
	if systemCAPool != nil {
		roots = systemCAPool.Clone()
	}
	intermediates := x509.NewCertPool()

	for _, candidate := range assetsByID {
		if candidate.Asset.ID == asset.Asset.ID || candidate.Asset.AssetType != AssetTypeCA {
			continue
		}
		addCertificateToPools(candidate.Leaf, roots, intermediates)
		for _, cert := range candidate.Certificates[1:] {
			addCertificateToPools(cert, roots, intermediates)
		}
	}

	for _, cert := range asset.Certificates[1:] {
		addCertificateToPools(cert, roots, intermediates)
	}

	visited := make(map[int64]struct{})
	for current := asset; current.Asset.HasIssuer(); {
		issuerAssetID := *current.Asset.IssuerAssetID
		if _, seen := visited[issuerAssetID]; seen {
			return fmt.Errorf("issuer chain contains a cycle")
		}
		visited[issuerAssetID] = struct{}{}

		issuer := assetsByID[issuerAssetID]
		addCertificateToPools(issuer.Leaf, roots, intermediates)
		for _, cert := range issuer.Certificates[1:] {
			addCertificateToPools(cert, roots, intermediates)
		}
		current = issuer
	}

	if asset.Asset.AssetType == AssetTypeCA && isSelfSigned(asset.Leaf) {
		roots.AddCert(asset.Leaf)
	}

	if _, err := asset.Leaf.Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: intermediates,
		CurrentTime:   now,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}); err != nil {
		return fmt.Errorf("verify certificate chain: %w", err)
	}
	return nil
}

func addCertificateToPools(cert *x509.Certificate, roots *x509.CertPool, intermediates *x509.CertPool) {
	if cert == nil {
		return
	}
	if isSelfSigned(cert) {
		roots.AddCert(cert)
		return
	}
	intermediates.AddCert(cert)
}

func isSelfSigned(cert *x509.Certificate) bool {
	if cert == nil {
		return false
	}
	if !bytes.Equal(cert.RawIssuer, cert.RawSubject) {
		return false
	}
	return cert.CheckSignatureFrom(cert) == nil
}

func parseCertificatesPEM(value string) ([]*x509.Certificate, error) {
	rest := []byte(value)
	certs := make([]*x509.Certificate, 0)

	for {
		rest = bytes.TrimSpace(rest)
		if len(rest) == 0 {
			break
		}

		block, next := pem.Decode(rest)
		if block == nil {
			return nil, fmt.Errorf("invalid pem data")
		}
		if block.Type != "CERTIFICATE" {
			return nil, fmt.Errorf("unsupported pem block %q in crt", block.Type)
		}

		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse certificate: %w", err)
		}

		certs = append(certs, cert)
		rest = next
	}

	return certs, nil
}

func computeCertificateHash(certs []*x509.Certificate) string {
	hash := sha256.New()
	for _, cert := range certs {
		_, _ = hash.Write(cert.Raw)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func wrapAssetError(asset Asset, err error) error {
	return fmt.Errorf("certificate asset %d (%s): %w", asset.ID, asset.Name, err)
}
