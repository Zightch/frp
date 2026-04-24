package certassets

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/zightch/frp/frps/internal/storage"
)

type ServiceOptions struct {
	Prepare PrepareOptions
	Now     func() time.Time
}

type Service struct {
	store   *storage.SQL
	prepare PrepareOptions
	now     func() time.Time
}

type CreateInput struct {
	Name   string
	Remark string
	Source Source
	CRT    string
	Key    string
}

type GenerateInput struct {
	Name          string
	Remark        string
	AssetType     AssetType
	IssuerAssetID *int64
	CommonName    string
	ValidityDays  int
	DNSNames      []string
	IPAddresses   []string
}

type ValidationIssue struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ValidationError struct {
	Message string
	Issues  []ValidationIssue
}

func (e *ValidationError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	if len(e.Issues) == 0 {
		return "certificate asset validation failed"
	}
	return e.Issues[0].Message
}

type NameConflictError struct {
	Name string
}

func (e *NameConflictError) Error() string {
	if e == nil {
		return ""
	}
	return "certificate asset name already exists"
}

type DuplicateAsset struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	AssetType AssetType `json:"asset_type"`
}

type DuplicateContentError struct {
	Message    string
	Duplicates []DuplicateAsset
}

func (e *DuplicateContentError) Error() string {
	if e == nil {
		return ""
	}
	if strings.TrimSpace(e.Message) != "" {
		return e.Message
	}
	return "certificate asset content already exists"
}

type DeleteImpactItem struct {
	Item  DescribedAsset `json:"item"`
	Depth int            `json:"depth"`
}

type DeleteImpact struct {
	Target   DescribedAsset     `json:"target"`
	Affected []DeleteImpactItem `json:"affected"`
}

type DeleteRequiresConfirmationError struct {
	Impact DeleteImpact
}

func (e *DeleteRequiresConfirmationError) Error() string {
	if e == nil {
		return ""
	}
	return "deleting this certificate will affect child certificates"
}

type DeleteResult struct {
	DeletedIDs []int64
}

func NewService(store *storage.SQL, options ServiceOptions) *Service {
	nowFn := options.Now
	if nowFn == nil {
		nowFn = func() time.Time { return time.Now().UTC() }
	}
	return &Service{
		store:   store,
		prepare: options.Prepare,
		now:     nowFn,
	}
}

func (s *Service) List(ctx context.Context) ([]DescribedAsset, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("certificate asset service store is nil")
	}

	assets, err := ListAssetsWithConn(ctx, s.store)
	if err != nil {
		return nil, err
	}
	prepared, _, err := PrepareAssetsWithOptions(assets, s.prepareOptionsAt(s.now()))
	if err != nil {
		return nil, err
	}
	return DescribePreparedAssets(prepared), nil
}

func (s *Service) Import(ctx context.Context, input CreateInput) (DescribedAsset, error) {
	var created DescribedAsset
	err := s.withTx(ctx, func(tx *storage.Tx, existing []Asset, preparedExisting []PreparedAsset, now time.Time) error {
		candidate, err := buildImportedAsset(existing, preparedExisting, input)
		if err != nil {
			return err
		}
		if err := ensureNameAvailable(existing, candidate.Name); err != nil {
			return err
		}
		if err := ensureNoDuplicateContent(ctx, tx, candidate); err != nil {
			return err
		}

		preparedAll, _, err := PrepareAssetsWithOptions(append(cloneAssets(existing), candidate), s.prepareOptionsAt(now))
		if err != nil {
			return validationErrorFromPrepare(candidate, err)
		}

		created, err = insertAndDescribeAsset(ctx, tx, candidate, preparedAll, now)
		return err
	})
	return created, err
}

func (s *Service) Generate(ctx context.Context, input GenerateInput) (DescribedAsset, error) {
	var created DescribedAsset
	err := s.withTx(ctx, func(tx *storage.Tx, existing []Asset, preparedExisting []PreparedAsset, now time.Time) error {
		candidate, err := buildGeneratedAsset(existing, preparedExisting, input, now)
		if err != nil {
			return err
		}
		if err := ensureNameAvailable(existing, candidate.Name); err != nil {
			return err
		}
		if err := ensureNoDuplicateContent(ctx, tx, candidate); err != nil {
			return err
		}

		preparedAll, _, err := PrepareAssetsWithOptions(append(cloneAssets(existing), candidate), s.prepareOptionsAt(now))
		if err != nil {
			return validationErrorFromPrepare(candidate, err)
		}

		created, err = insertAndDescribeAsset(ctx, tx, candidate, preparedAll, now)
		return err
	})
	return created, err
}

func (s *Service) DeleteImpact(ctx context.Context, id int64) (DeleteImpact, error) {
	if s == nil || s.store == nil {
		return DeleteImpact{}, fmt.Errorf("certificate asset service store is nil")
	}
	assets, err := ListAssetsWithConn(ctx, s.store)
	if err != nil {
		return DeleteImpact{}, err
	}
	prepared, _, err := PrepareAssetsWithOptions(assets, s.prepareOptionsAt(s.now()))
	if err != nil {
		return DeleteImpact{}, err
	}
	return buildDeleteImpact(prepared, id)
}

func (s *Service) Delete(ctx context.Context, id int64, cascade bool) (DeleteResult, error) {
	var result DeleteResult
	err := s.withTx(ctx, func(tx *storage.Tx, existing []Asset, preparedExisting []PreparedAsset, _ time.Time) error {
		impact, err := buildDeleteImpact(preparedExisting, id)
		if err != nil {
			return err
		}
		if len(impact.Affected) > 0 && !cascade {
			return &DeleteRequiresConfirmationError{Impact: impact}
		}

		result.DeletedIDs = make([]int64, 0, 1+len(impact.Affected))
		result.DeletedIDs = append(result.DeletedIDs, impact.Target.ID)
		for _, item := range impact.Affected {
			result.DeletedIDs = append(result.DeletedIDs, item.Item.ID)
		}

		if err := DeleteAssetsByID(ctx, tx, result.DeletedIDs); err != nil {
			return err
		}
		return nil
	})
	return result, err
}

func (s *Service) withTx(ctx context.Context, fn func(tx *storage.Tx, existing []Asset, preparedExisting []PreparedAsset, now time.Time) error) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("certificate asset service store is nil")
	}

	return s.store.WithTxContext(ctx, nil, func(tx *storage.Tx) error {
		existing, err := ListAssetsWithConn(ctx, tx)
		if err != nil {
			return err
		}
		now := s.now()
		preparedExisting, _, err := PrepareAssetsWithOptions(existing, s.prepareOptionsAt(now))
		if err != nil {
			return err
		}
		return fn(tx, existing, preparedExisting, now)
	})
}

func (s *Service) prepareOptionsAt(now time.Time) PrepareOptions {
	options := s.prepare
	options.Now = now.UTC()
	return options
}

func buildImportedAsset(existing []Asset, preparedExisting []PreparedAsset, input CreateInput) (Asset, error) {
	name := strings.TrimSpace(input.Name)
	remark := strings.TrimSpace(input.Remark)
	if name == "" {
		return Asset{}, validationError("name is required", ValidationIssue{
			Field:   "name",
			Code:    "required",
			Message: "name is required",
		})
	}

	crt := normalizePEMText(input.CRT)
	if crt == "" {
		return Asset{}, validationError("crt is required", ValidationIssue{
			Field:   "crt",
			Code:    "required",
			Message: "crt is required",
		})
	}

	crtHash, err := ComputeCRTHash(crt)
	if err != nil {
		return Asset{}, validationError("crt must be valid PEM certificate content", ValidationIssue{
			Field:   "crt",
			Code:    "invalid_crt_pem",
			Message: "crt must be valid PEM certificate content",
		})
	}

	certs, err := parseCertificatesPEM(crt)
	if err != nil {
		return Asset{}, validationError("crt must be valid PEM certificate content", ValidationIssue{
			Field:   "crt",
			Code:    "invalid_crt_pem",
			Message: "crt must be valid PEM certificate content",
		})
	}
	if len(certs) == 0 || certs[0] == nil {
		return Asset{}, validationError("crt must contain at least one certificate", ValidationIssue{
			Field:   "crt",
			Code:    "empty_certificate_chain",
			Message: "crt must contain at least one certificate",
		})
	}

	assetType := inferImportedAssetType(certs[0])
	issuerAssetID := inferImportedIssuerAssetID(preparedExisting, certs)

	source := input.Source
	if source == "" {
		source = SourceUpload
	}
	if source != SourceUpload {
		return Asset{}, validationError("source must be upload", ValidationIssue{
			Field:   "source",
			Code:    "invalid_source",
			Message: "source must be upload",
		})
	}

	candidate := Asset{
		ID:            nextCandidateID(existing),
		Name:          name,
		Remark:        remark,
		Source:        source,
		AssetType:     assetType,
		FormatType:    FormatTypePEM,
		CRT:           crt,
		CRTHash:       crtHash,
		Key:           normalizePEMText(input.Key),
		IssuerAssetID: issuerAssetID,
	}
	return candidate, nil
}

func inferImportedAssetType(leaf *x509.Certificate) AssetType {
	if leaf != nil && leaf.IsCA {
		return AssetTypeCA
	}
	return AssetTypeCertificate
}

func inferImportedIssuerAssetID(preparedExisting []PreparedAsset, certs []*x509.Certificate) *int64 {
	if len(certs) == 0 || certs[0] == nil || isSelfSigned(certs[0]) {
		return nil
	}

	candidates := make([]PreparedAsset, 0)
	for _, item := range preparedExisting {
		if item.Asset.AssetType != AssetTypeCA || item.Leaf == nil {
			continue
		}
		if certs[0].CheckSignatureFrom(item.Leaf) != nil {
			continue
		}
		candidates = append(candidates, item)
	}
	if len(candidates) == 0 {
		return nil
	}

	candidates = narrowIssuerCandidatesByEmbeddedChain(candidates, certs)
	candidates = narrowIssuerCandidatesByAuthorityKeyID(candidates, certs[0])
	candidates = narrowIssuerCandidatesByKeyPresence(candidates)
	sort.Slice(candidates, func(left, right int) bool {
		return candidates[left].Asset.ID < candidates[right].Asset.ID
	})

	issuerAssetID := candidates[0].Asset.ID
	return &issuerAssetID
}

func narrowIssuerCandidatesByEmbeddedChain(candidates []PreparedAsset, certs []*x509.Certificate) []PreparedAsset {
	if len(candidates) == 0 || len(certs) < 2 || certs[1] == nil {
		return candidates
	}

	matches := make([]PreparedAsset, 0)
	for _, item := range candidates {
		if item.Leaf != nil && bytes.Equal(item.Leaf.Raw, certs[1].Raw) {
			matches = append(matches, item)
		}
	}
	if len(matches) == 0 {
		return candidates
	}
	return matches
}

func narrowIssuerCandidatesByAuthorityKeyID(candidates []PreparedAsset, leaf *x509.Certificate) []PreparedAsset {
	if len(candidates) == 0 || leaf == nil || len(leaf.AuthorityKeyId) == 0 {
		return candidates
	}

	matches := make([]PreparedAsset, 0)
	for _, item := range candidates {
		if item.Leaf != nil && bytes.Equal(item.Leaf.SubjectKeyId, leaf.AuthorityKeyId) {
			matches = append(matches, item)
		}
	}
	if len(matches) == 0 {
		return candidates
	}
	return matches
}

func narrowIssuerCandidatesByKeyPresence(candidates []PreparedAsset) []PreparedAsset {
	if len(candidates) <= 1 {
		return candidates
	}

	matches := make([]PreparedAsset, 0)
	for _, item := range candidates {
		if item.Asset.HasKey() {
			matches = append(matches, item)
		}
	}
	if len(matches) == 0 {
		return candidates
	}
	return matches
}

func buildGeneratedAsset(existing []Asset, preparedExisting []PreparedAsset, input GenerateInput, now time.Time) (Asset, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return Asset{}, validationError("name is required", ValidationIssue{
			Field:   "name",
			Code:    "required",
			Message: "name is required",
		})
	}

	commonName := strings.TrimSpace(input.CommonName)
	if commonName == "" {
		return Asset{}, validationError("common_name is required", ValidationIssue{
			Field:   "common_name",
			Code:    "required",
			Message: "common_name is required",
		})
	}

	assetType := normalizeAssetType(input.AssetType)
	if assetType == "" {
		return Asset{}, validationError("asset_type must be certificate or ca", ValidationIssue{
			Field:   "asset_type",
			Code:    "invalid_asset_type",
			Message: "asset_type must be certificate or ca",
		})
	}

	if input.ValidityDays <= 0 || input.ValidityDays > 3650 {
		return Asset{}, validationError("validity_days must be between 1 and 3650", ValidationIssue{
			Field:   "validity_days",
			Code:    "invalid_validity_days",
			Message: "validity_days must be between 1 and 3650",
		})
	}

	issuerAssetID := normalizeIssuerAssetID(input.IssuerAssetID)
	var issuer PreparedAsset
	var hasIssuer bool
	if issuerAssetID != nil {
		item, ok := FindPreparedAssetByID(preparedExisting, *issuerAssetID)
		if !ok {
			return Asset{}, validationError("issuer_asset_id does not exist", ValidationIssue{
				Field:   "issuer_asset_id",
				Code:    "issuer_not_found",
				Message: "issuer_asset_id does not exist",
			})
		}
		if item.Asset.AssetType != AssetTypeCA {
			return Asset{}, validationError("issuer_asset_id must reference a ca asset", ValidationIssue{
				Field:   "issuer_asset_id",
				Code:    "issuer_not_ca",
				Message: "issuer_asset_id must reference a ca asset",
			})
		}
		if !item.Asset.HasKey() {
			return Asset{}, validationError("issuer ca asset requires key to sign new certificates", ValidationIssue{
				Field:   "issuer_asset_id",
				Code:    "issuer_missing_key",
				Message: "issuer ca asset requires key to sign new certificates",
			})
		}
		issuer = item
		hasIssuer = true
	}

	if assetType == AssetTypeCertificate && !hasIssuer {
		return Asset{}, validationError("certificate generation requires issuer_asset_id", ValidationIssue{
			Field:   "issuer_asset_id",
			Code:    "issuer_required",
			Message: "certificate generation requires issuer_asset_id",
		})
	}

	dnsNames := make([]string, 0, len(input.DNSNames))
	for _, raw := range input.DNSNames {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		dnsNames = append(dnsNames, value)
	}

	ipAddresses := make([]net.IP, 0, len(input.IPAddresses))
	for _, raw := range input.IPAddresses {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		ip := net.ParseIP(value)
		if ip == nil {
			return Asset{}, validationError("ip_addresses contains invalid IP", ValidationIssue{
				Field:   "ip_addresses",
				Code:    "invalid_ip",
				Message: "ip_addresses contains invalid IP",
			})
		}
		ipAddresses = append(ipAddresses, ip)
	}

	crt, key, err := generatePEMMaterial(generateMaterialInput{
		AssetType:    assetType,
		CommonName:   commonName,
		ValidityDays: input.ValidityDays,
		DNSNames:     dnsNames,
		IPAddresses:  ipAddresses,
		Issuer:       issuer,
		HasIssuer:    hasIssuer,
		Now:          now.UTC(),
	})
	if err != nil {
		return Asset{}, err
	}

	crtHash, err := ComputeCRTHash(crt)
	if err != nil {
		return Asset{}, err
	}

	return Asset{
		ID:            nextCandidateID(existing),
		Name:          name,
		Remark:        strings.TrimSpace(input.Remark),
		Source:        SourceGenerated,
		AssetType:     assetType,
		FormatType:    FormatTypePEM,
		CRT:           crt,
		CRTHash:       crtHash,
		Key:           key,
		IssuerAssetID: issuerAssetID,
	}, nil
}

type generateMaterialInput struct {
	AssetType    AssetType
	CommonName   string
	ValidityDays int
	DNSNames     []string
	IPAddresses  []net.IP
	Issuer       PreparedAsset
	HasIssuer    bool
	Now          time.Time
}

func generatePEMMaterial(input generateMaterialInput) (string, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generate private key: %w", err)
	}

	notBefore := input.Now.Add(-5 * time.Minute)
	if input.HasIssuer && input.Issuer.Leaf != nil && notBefore.Before(input.Issuer.Leaf.NotBefore) {
		notBefore = input.Issuer.Leaf.NotBefore
	}
	notAfter := input.Now.Add(time.Duration(input.ValidityDays) * 24 * time.Hour)
	if input.HasIssuer && input.Issuer.Leaf != nil && notAfter.After(input.Issuer.Leaf.NotAfter) {
		return "", "", validationError("requested validity exceeds issuer certificate lifetime", ValidationIssue{
			Field:   "validity_days",
			Code:    "validity_exceeds_issuer",
			Message: "requested validity exceeds issuer certificate lifetime",
		})
	}

	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return "", "", fmt.Errorf("generate certificate serial: %w", err)
	}

	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName: input.CommonName,
		},
		BasicConstraintsValid: true,
		NotBefore:             notBefore,
		NotAfter:              notAfter,
	}

	if input.AssetType == AssetTypeCA {
		template.IsCA = true
		template.KeyUsage = x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign | x509.KeyUsageCRLSign
	} else {
		template.KeyUsage = x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment
		template.ExtKeyUsage = []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
			x509.ExtKeyUsageClientAuth,
		}
		template.DNSNames = append([]string(nil), input.DNSNames...)
		template.IPAddresses = append([]net.IP(nil), input.IPAddresses...)
	}

	parent := template
	signer := any(key)
	if input.HasIssuer {
		parent = input.Issuer.Leaf
		signer, err = parsePrivateKeyPEM(input.Issuer.Asset.Key)
		if err != nil {
			return "", "", validationError("issuer key is invalid", ValidationIssue{
				Field:   "issuer_asset_id",
				Code:    "issuer_key_invalid",
				Message: "issuer key is invalid",
			})
		}
	}

	der, err := x509.CreateCertificate(rand.Reader, template, parent, key.Public(), signer)
	if err != nil {
		return "", "", fmt.Errorf("create certificate: %w", err)
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", "", fmt.Errorf("marshal private key: %w", err)
	}

	crt := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	return normalizePEMText(crt), normalizePEMText(keyPEM), nil
}

func insertAndDescribeAsset(ctx context.Context, tx *storage.Tx, candidate Asset, preparedAll []PreparedAsset, now time.Time) (DescribedAsset, error) {
	candidate.CreatedAt = now.UTC()
	candidate.UpdatedAt = now.UTC()

	insertID, err := InsertAsset(ctx, tx, candidate)
	if err != nil {
		return DescribedAsset{}, err
	}

	preparedCandidate, ok := FindPreparedAssetByID(preparedAll, candidate.ID)
	if !ok {
		return DescribedAsset{}, fmt.Errorf("prepared candidate asset %d not found", candidate.ID)
	}
	preparedCandidate.Asset.ID = insertID
	preparedCandidate.Asset.CreatedAt = candidate.CreatedAt
	preparedCandidate.Asset.UpdatedAt = candidate.UpdatedAt

	issuerNames := make(map[int64]string, len(preparedAll))
	for _, item := range preparedAll {
		issuerNames[item.Asset.ID] = item.Asset.Name
	}
	return describePreparedAsset(preparedCandidate, issuerNames), nil
}

func ensureNameAvailable(existing []Asset, name string) error {
	for _, asset := range existing {
		if asset.Name == name {
			return &NameConflictError{Name: name}
		}
	}
	return nil
}

func ensureNoDuplicateContent(ctx context.Context, conn storage.Conn, candidate Asset) error {
	items, err := ListAssetsByCRTHash(ctx, conn, candidate.CRTHash)
	if err != nil {
		return err
	}

	duplicates := make([]DuplicateAsset, 0)
	for _, item := range items {
		if SameAssetContent(item, candidate) {
			duplicates = append(duplicates, DuplicateAsset{
				ID:        item.ID,
				Name:      item.Name,
				AssetType: item.AssetType,
			})
		}
	}
	if len(duplicates) == 0 {
		return nil
	}
	return &DuplicateContentError{
		Message:    "certificate asset content already exists",
		Duplicates: duplicates,
	}
}

func buildDeleteImpact(prepared []PreparedAsset, id int64) (DeleteImpact, error) {
	target, ok := FindPreparedAssetByID(prepared, id)
	if !ok {
		return DeleteImpact{}, sql.ErrNoRows
	}

	described := DescribePreparedAssets(prepared)
	describedByID := make(map[int64]DescribedAsset, len(described))
	for _, item := range described {
		describedByID[item.ID] = item
	}

	childrenByID := make(map[int64][]PreparedAsset)
	for _, item := range prepared {
		if !item.Asset.HasIssuer() {
			continue
		}
		parentID := *item.Asset.IssuerAssetID
		childrenByID[parentID] = append(childrenByID[parentID], item)
	}
	for parentID := range childrenByID {
		sort.Slice(childrenByID[parentID], func(left, right int) bool {
			return childrenByID[parentID][left].Asset.ID < childrenByID[parentID][right].Asset.ID
		})
	}

	affected := make([]DeleteImpactItem, 0)
	var walk func(parentID int64, depth int)
	walk = func(parentID int64, depth int) {
		for _, child := range childrenByID[parentID] {
			affected = append(affected, DeleteImpactItem{
				Item:  describedByID[child.Asset.ID],
				Depth: depth,
			})
			walk(child.Asset.ID, depth+1)
		}
	}
	walk(id, 1)

	return DeleteImpact{
		Target:   describedByID[target.Asset.ID],
		Affected: affected,
	}, nil
}

func validationError(message string, issues ...ValidationIssue) error {
	return &ValidationError{
		Message: message,
		Issues:  append([]ValidationIssue(nil), issues...),
	}
}

func validationErrorFromPrepare(candidate Asset, err error) error {
	if err == nil {
		return nil
	}

	prefix := fmt.Sprintf("certificate asset %d (%s): ", candidate.ID, candidate.Name)
	lines := strings.Split(strings.TrimSpace(err.Error()), "\n")
	issues := make([]ValidationIssue, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), prefix))
		if line == "" {
			continue
		}
		issues = append(issues, ValidationIssue{
			Field:   inferIssueField(line),
			Code:    inferIssueCode(line),
			Message: line,
		})
	}
	if len(issues) == 0 {
		issues = append(issues, ValidationIssue{
			Field:   "crt",
			Code:    "validation_failed",
			Message: err.Error(),
		})
	}
	return &ValidationError{
		Message: issues[0].Message,
		Issues:  issues,
	}
}

func inferIssueField(message string) string {
	switch {
	case strings.Contains(message, "source"):
		return "source"
	case strings.Contains(message, "asset_type"):
		return "asset_type"
	case strings.Contains(message, "format_type"):
		return "format_type"
	case strings.Contains(message, "issuer"):
		return "issuer_asset_id"
	case strings.Contains(message, "key"):
		return "key"
	default:
		return "crt"
	}
}

func inferIssueCode(message string) string {
	switch {
	case strings.Contains(message, "expired at"), strings.Contains(message, "not valid before"), strings.Contains(message, "invalid time range"):
		return "certificate_time_invalid"
	case strings.Contains(message, "crt_hash mismatch"):
		return "crt_hash_mismatch"
	case strings.Contains(message, "issuer"):
		return "issuer_invalid"
	case strings.Contains(message, "key"):
		return "key_invalid"
	case strings.Contains(message, "pem"):
		return "invalid_pem"
	default:
		return "validation_failed"
	}
}

func normalizeAssetType(value AssetType) AssetType {
	switch AssetType(strings.ToLower(strings.TrimSpace(string(value)))) {
	case AssetTypeCertificate:
		return AssetTypeCertificate
	case AssetTypeCA:
		return AssetTypeCA
	default:
		return ""
	}
}

func normalizeIssuerAssetID(value *int64) *int64 {
	if value == nil || *value <= 0 {
		return nil
	}
	normalized := *value
	return &normalized
}

func normalizePEMText(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return value + "\n"
}

func nextCandidateID(existing []Asset) int64 {
	var maxID int64
	for _, item := range existing {
		if item.ID > maxID {
			maxID = item.ID
		}
	}
	if maxID < 1 {
		return 1
	}
	return maxID + 1
}

func cloneAssets(items []Asset) []Asset {
	if len(items) == 0 {
		return nil
	}
	cloned := make([]Asset, len(items))
	copy(cloned, items)
	return cloned
}
