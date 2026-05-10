package api

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zightch/frp/frps/internal/certassets"
)

func TestCertificateAssetsLifecycle(t *testing.T) {
	store := newTestStore(t)
	manager := newTestAuthManager(t, true)

	server, err := NewServer(
		Options{
			Addr:              "127.0.0.1:7080",
			ReadHeaderTimeout: 5 * time.Second,
			Store:             store,
			Auth:              manager,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test",
	)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	sessionCookie := authenticatedManagementCookie(t, manager)

	root := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/certificate-assets/generate",
		map[string]any{
			"name":          "root-ca",
			"asset_type":    "ca",
			"common_name":   "Root CA",
			"validity_days": 365,
		},
		http.StatusCreated,
		sessionCookie,
	)
	rootItem := root.JSON["item"].(map[string]any)
	rootID := int64(rootItem["id"].(float64))
	if rootItem["can_issue"] != true {
		t.Fatalf("expected generated root ca to be issuable: %#v", rootItem)
	}

	leaf := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/certificate-assets/generate",
		map[string]any{
			"name":            "leaf-cert",
			"asset_type":      "certificate",
			"issuer_asset_id": rootID,
			"common_name":     "leaf.example.com",
			"validity_days":   30,
			"dns_names":       []string{"leaf.example.com"},
			"ip_addresses":    []string{"127.0.0.1"},
		},
		http.StatusCreated,
		sessionCookie,
	)
	leafItem := leaf.JSON["item"].(map[string]any)
	if int64(leafItem["issuer_asset_id"].(float64)) != rootID {
		t.Fatalf("unexpected issuer_asset_id: %#v", leafItem)
	}

	list := performRequest(
		t,
		server.Handler(),
		http.MethodGet,
		"/api/v1/certificate-assets",
		nil,
		http.StatusOK,
		sessionCookie,
	)
	items := list.JSON["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("unexpected asset count: %#v", list.JSON)
	}

	downloadOptions := performRequest(
		t,
		server.Handler(),
		http.MethodGet,
		"/api/v1/certificate-assets/"+strconv.FormatInt(rootID, 10)+"/download-options",
		nil,
		http.StatusOK,
		sessionCookie,
	)
	modes := downloadOptions.JSON["modes"].([]any)
	if len(modes) != 3 {
		t.Fatalf("unexpected generated ca download modes: %#v", downloadOptions.JSON)
	}
	treeItems := downloadOptions.JSON["tree_items"].([]any)
	if len(treeItems) != 2 {
		t.Fatalf("unexpected generated ca tree items: %#v", downloadOptions.JSON)
	}

	impact := performRequest(
		t,
		server.Handler(),
		http.MethodGet,
		"/api/v1/certificate-assets/"+strconv.FormatInt(rootID, 10)+"/delete-impact",
		nil,
		http.StatusOK,
		sessionCookie,
	)
	if impact.JSON["requires_confirmation"] != true {
		t.Fatalf("expected delete impact confirmation: %#v", impact.JSON)
	}
	affectedItems := impact.JSON["affected_items"].([]any)
	if len(affectedItems) != 1 {
		t.Fatalf("unexpected affected items: %#v", impact.JSON)
	}

	deleteBlocked := performRequest(
		t,
		server.Handler(),
		http.MethodDelete,
		"/api/v1/certificate-assets/"+strconv.FormatInt(rootID, 10),
		nil,
		http.StatusConflict,
		sessionCookie,
	)
	if deleteBlocked.JSON["error_code"] != "certificate_asset_delete_requires_confirmation" {
		t.Fatalf("unexpected delete blocked payload: %#v", deleteBlocked.JSON)
	}

	deleted := performRequest(
		t,
		server.Handler(),
		http.MethodDelete,
		"/api/v1/certificate-assets/"+strconv.FormatInt(rootID, 10)+"?cascade=true",
		nil,
		http.StatusOK,
		sessionCookie,
	)
	if deleted.JSON["deleted"] != true {
		t.Fatalf("unexpected delete response: %#v", deleted.JSON)
	}

	listAfterDelete := performRequest(
		t,
		server.Handler(),
		http.MethodGet,
		"/api/v1/certificate-assets",
		nil,
		http.StatusOK,
		sessionCookie,
	)
	if len(listAfterDelete.JSON["items"].([]any)) != 0 {
		t.Fatalf("expected empty asset list after delete: %#v", listAfterDelete.JSON)
	}
}

func TestCertificateAssetsExpiredAssetStillListsAndDeletes(t *testing.T) {
	store := newTestStore(t)
	manager := newTestAuthManager(t, true)

	server, err := NewServer(
		Options{
			Addr:              "127.0.0.1:7080",
			ReadHeaderTimeout: 5 * time.Second,
			Store:             store,
			Auth:              manager,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test",
	)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	sessionCookie := authenticatedManagementCookie(t, manager)
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	expired := issueAPITestCertificate(t, apiTestCertificateSpec{
		CommonName: "staticplant.top",
		NotBefore:  now.Add(-48 * time.Hour),
		NotAfter:   now.Add(-12 * time.Hour),
	})
	crtHash, err := certassets.ComputeCRTHash(expired.CertPEM)
	if err != nil {
		t.Fatalf("compute crt hash: %v", err)
	}
	expiredID, err := certassets.InsertAsset(context.Background(), store, certassets.Asset{
		Name:       "staticplant.top",
		Source:     certassets.SourceUpload,
		AssetType:  certassets.AssetTypeCertificate,
		FormatType: certassets.FormatTypePEM,
		CRT:        expired.CertPEM,
		CRTHash:    crtHash,
		Key:        expired.KeyPEM,
		CreatedAt:  now,
		UpdatedAt:  now,
	})
	if err != nil {
		t.Fatalf("insert expired asset: %v", err)
	}

	list := performRequest(
		t,
		server.Handler(),
		http.MethodGet,
		"/api/v1/certificate-assets",
		nil,
		http.StatusOK,
		sessionCookie,
	)
	items := list.JSON["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("unexpected asset count: %#v", list.JSON)
	}
	item := items[0].(map[string]any)
	if int64(item["id"].(float64)) != expiredID {
		t.Fatalf("unexpected listed asset: %#v", item)
	}
	if item["not_after"] == "" {
		t.Fatalf("expected expired asset to keep not_after metadata: %#v", item)
	}

	impact := performRequest(
		t,
		server.Handler(),
		http.MethodGet,
		"/api/v1/certificate-assets/"+strconv.FormatInt(expiredID, 10)+"/delete-impact",
		nil,
		http.StatusOK,
		sessionCookie,
	)
	if impact.JSON["target"].(map[string]any)["id"].(float64) != float64(expiredID) {
		t.Fatalf("unexpected delete impact target: %#v", impact.JSON)
	}

	deleted := performRequest(
		t,
		server.Handler(),
		http.MethodDelete,
		"/api/v1/certificate-assets/"+strconv.FormatInt(expiredID, 10),
		nil,
		http.StatusOK,
		sessionCookie,
	)
	if deleted.JSON["deleted"] != true {
		t.Fatalf("unexpected delete response: %#v", deleted.JSON)
	}

	listAfterDelete := performRequest(
		t,
		server.Handler(),
		http.MethodGet,
		"/api/v1/certificate-assets",
		nil,
		http.StatusOK,
		sessionCookie,
	)
	if len(listAfterDelete.JSON["items"].([]any)) != 0 {
		t.Fatalf("expected empty asset list after delete: %#v", listAfterDelete.JSON)
	}
}

func TestCertificateAssetsGenerateSupportsCustomKeySpec(t *testing.T) {
	store := newTestStore(t)
	manager := newTestAuthManager(t, true)

	server, err := NewServer(
		Options{
			Addr:              "127.0.0.1:7080",
			ReadHeaderTimeout: 5 * time.Second,
			Store:             store,
			Auth:              manager,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test",
	)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	sessionCookie := authenticatedManagementCookie(t, manager)

	root := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/certificate-assets/generate",
		map[string]any{
			"name":          "rsa-root-ca",
			"asset_type":    "ca",
			"common_name":   "RSA Root CA",
			"validity_days": 365,
			"key_algorithm": "rsa",
			"key_bits":      3072,
		},
		http.StatusCreated,
		sessionCookie,
	)
	rootID := int64(root.JSON["item"].(map[string]any)["id"].(float64))

	leaf := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/certificate-assets/generate",
		map[string]any{
			"name":            "ecdsa-leaf-cert",
			"asset_type":      "certificate",
			"issuer_asset_id": rootID,
			"common_name":     "leaf.example.com",
			"validity_days":   30,
			"key_algorithm":   "ecdsa",
			"key_bits":        384,
		},
		http.StatusCreated,
		sessionCookie,
	)
	leafID := int64(leaf.JSON["item"].(map[string]any)["id"].(float64))

	ed25519Leaf := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/certificate-assets/generate",
		map[string]any{
			"name":            "ed25519-leaf-cert",
			"asset_type":      "certificate",
			"issuer_asset_id": rootID,
			"common_name":     "ed25519.example.com",
			"validity_days":   30,
			"key_algorithm":   "ed25519",
			"key_bits":        256,
		},
		http.StatusCreated,
		sessionCookie,
	)
	ed25519LeafID := int64(ed25519Leaf.JSON["item"].(map[string]any)["id"].(float64))

	rootAsset, err := certassets.LoadAssetByID(context.Background(), store, rootID)
	if err != nil {
		t.Fatalf("load generated root asset: %v", err)
	}
	rootCert := parseSingleCertificatePEM(t, rootAsset.CRT)
	if rootCert.PublicKeyAlgorithm != x509.RSA {
		t.Fatalf("expected generated root certificate to use RSA, got %v", rootCert.PublicKeyAlgorithm)
	}
	rootKey, ok := parsePKCS8PrivateKeyPEM(t, rootAsset.Key).(*rsa.PrivateKey)
	if !ok {
		t.Fatalf("expected generated root key to be RSA")
	}
	if bits := rootKey.N.BitLen(); bits != 3072 {
		t.Fatalf("expected generated root key bits 3072, got %d", bits)
	}

	leafAsset, err := certassets.LoadAssetByID(context.Background(), store, leafID)
	if err != nil {
		t.Fatalf("load generated leaf asset: %v", err)
	}
	leafCert := parseSingleCertificatePEM(t, leafAsset.CRT)
	if leafCert.PublicKeyAlgorithm != x509.ECDSA {
		t.Fatalf("expected generated leaf certificate to use ECDSA, got %v", leafCert.PublicKeyAlgorithm)
	}
	leafKey, ok := parsePKCS8PrivateKeyPEM(t, leafAsset.Key).(*ecdsa.PrivateKey)
	if !ok {
		t.Fatalf("expected generated leaf key to be ECDSA")
	}
	if bits := leafKey.Curve.Params().BitSize; bits != 384 {
		t.Fatalf("expected generated leaf key bits 384, got %d", bits)
	}

	ed25519Asset, err := certassets.LoadAssetByID(context.Background(), store, ed25519LeafID)
	if err != nil {
		t.Fatalf("load generated ed25519 leaf asset: %v", err)
	}
	ed25519Cert := parseSingleCertificatePEM(t, ed25519Asset.CRT)
	if ed25519Cert.PublicKeyAlgorithm != x509.Ed25519 {
		t.Fatalf("expected generated ed25519 certificate to use Ed25519, got %v", ed25519Cert.PublicKeyAlgorithm)
	}
	ed25519Key, ok := parsePKCS8PrivateKeyPEM(t, ed25519Asset.Key).(ed25519.PrivateKey)
	if !ok {
		t.Fatalf("expected generated ed25519 key to be Ed25519")
	}
	if seedSize := ed25519Key.Seed(); len(seedSize) != ed25519.SeedSize {
		t.Fatalf("expected generated ed25519 seed size %d, got %d", ed25519.SeedSize, len(seedSize))
	}
}

func TestCertificateAssetsGenerateRejectsInvalidKeyBits(t *testing.T) {
	store := newTestStore(t)
	manager := newTestAuthManager(t, true)

	server, err := NewServer(
		Options{
			Addr:              "127.0.0.1:7080",
			ReadHeaderTimeout: 5 * time.Second,
			Store:             store,
			Auth:              manager,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test",
	)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	sessionCookie := authenticatedManagementCookie(t, manager)
	response := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/certificate-assets/generate",
		map[string]any{
			"name":          "bad-rsa-ca",
			"asset_type":    "ca",
			"common_name":   "Bad RSA CA",
			"validity_days": 365,
			"key_algorithm": "rsa",
			"key_bits":      256,
		},
		http.StatusUnprocessableEntity,
		sessionCookie,
	)
	if response.JSON["error_code"] != "certificate_asset_validation_failed" {
		t.Fatalf("unexpected error code: %#v", response.JSON)
	}

	details := response.JSON["details"].(map[string]any)
	issues := details["issues"].([]any)
	if len(issues) == 0 {
		t.Fatalf("expected validation issues: %#v", response.JSON)
	}
	firstIssue := issues[0].(map[string]any)
	if firstIssue["field"] != "key_bits" {
		t.Fatalf("unexpected validation issue: %#v", firstIssue)
	}
}

func TestCertificateAssetsUpdateMetadata(t *testing.T) {
	store := newTestStore(t)
	manager := newTestAuthManager(t, true)

	server, err := NewServer(
		Options{
			Addr:              "127.0.0.1:7080",
			ReadHeaderTimeout: 5 * time.Second,
			Store:             store,
			Auth:              manager,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test",
	)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	sessionCookie := authenticatedManagementCookie(t, manager)

	created := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/certificate-assets/generate",
		map[string]any{
			"name":          "editable-ca",
			"remark":        "before",
			"asset_type":    "ca",
			"common_name":   "Editable CA",
			"validity_days": 365,
		},
		http.StatusCreated,
		sessionCookie,
	)
	assetID := int64(created.JSON["item"].(map[string]any)["id"].(float64))

	updated := performRequest(
		t,
		server.Handler(),
		http.MethodPatch,
		"/api/v1/certificate-assets/"+strconv.FormatInt(assetID, 10),
		map[string]any{
			"name":   "editable-ca-renamed",
			"remark": "after",
		},
		http.StatusOK,
		sessionCookie,
	)
	updatedItem := updated.JSON["item"].(map[string]any)
	if updatedItem["name"] != "editable-ca-renamed" {
		t.Fatalf("unexpected updated name: %#v", updated.JSON)
	}
	if updatedItem["remark"] != "after" {
		t.Fatalf("unexpected updated remark: %#v", updated.JSON)
	}

	list := performRequest(
		t,
		server.Handler(),
		http.MethodGet,
		"/api/v1/certificate-assets",
		nil,
		http.StatusOK,
		sessionCookie,
	)
	items := list.JSON["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("unexpected asset list after update: %#v", list.JSON)
	}
	item := items[0].(map[string]any)
	if item["name"] != "editable-ca-renamed" || item["remark"] != "after" {
		t.Fatalf("unexpected listed item after update: %#v", item)
	}
}

func TestCertificateAssetsUpdateMetadataRejectsNameConflict(t *testing.T) {
	store := newTestStore(t)
	manager := newTestAuthManager(t, true)

	server, err := NewServer(
		Options{
			Addr:              "127.0.0.1:7080",
			ReadHeaderTimeout: 5 * time.Second,
			Store:             store,
			Auth:              manager,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test",
	)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	sessionCookie := authenticatedManagementCookie(t, manager)

	first := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/certificate-assets/generate",
		map[string]any{
			"name":          "first-ca",
			"asset_type":    "ca",
			"common_name":   "First CA",
			"validity_days": 365,
		},
		http.StatusCreated,
		sessionCookie,
	)
	second := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/certificate-assets/generate",
		map[string]any{
			"name":          "second-ca",
			"asset_type":    "ca",
			"common_name":   "Second CA",
			"validity_days": 365,
		},
		http.StatusCreated,
		sessionCookie,
	)
	secondID := int64(second.JSON["item"].(map[string]any)["id"].(float64))

	conflict := performRequest(
		t,
		server.Handler(),
		http.MethodPatch,
		"/api/v1/certificate-assets/"+strconv.FormatInt(secondID, 10),
		map[string]any{
			"name":   first.JSON["item"].(map[string]any)["name"],
			"remark": "conflict",
		},
		http.StatusConflict,
		sessionCookie,
	)
	if conflict.JSON["error_code"] != "certificate_asset_name_conflict" {
		t.Fatalf("unexpected update conflict payload: %#v", conflict.JSON)
	}
}

func TestCertificateAssetsPasteRejectsInvalidContent(t *testing.T) {
	store := newTestStore(t)
	manager := newTestAuthManager(t, true)

	server, err := NewServer(
		Options{
			Addr:              "127.0.0.1:7080",
			ReadHeaderTimeout: 5 * time.Second,
			Store:             store,
			Auth:              manager,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test",
	)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	sessionCookie := authenticatedManagementCookie(t, manager)
	response := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/certificate-assets/paste",
		map[string]any{
			"name": "bad-cert",
			"crt":  "not a pem",
			"key":  "not a key",
		},
		http.StatusUnprocessableEntity,
		sessionCookie,
	)
	if response.JSON["error_code"] != "certificate_asset_validation_failed" {
		t.Fatalf("unexpected error code: %#v", response.JSON)
	}

	details := response.JSON["details"].(map[string]any)
	issues := details["issues"].([]any)
	if len(issues) == 0 {
		t.Fatalf("expected structured issues: %#v", response.JSON)
	}
	firstIssue := issues[0].(map[string]any)
	if firstIssue["field"] != "crt" {
		t.Fatalf("unexpected first issue: %#v", firstIssue)
	}
}

func TestCertificateAssetsPasteAutoDetectsTypeAndIssuer(t *testing.T) {
	store := newTestStore(t)
	manager := newTestAuthManager(t, true)

	server, err := NewServer(
		Options{
			Addr:              "127.0.0.1:7080",
			ReadHeaderTimeout: 5 * time.Second,
			Store:             store,
			Auth:              manager,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test",
	)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	sessionCookie := authenticatedManagementCookie(t, manager)
	now := time.Now().UTC()

	root := issueAPITestCertificate(t, apiTestCertificateSpec{
		CommonName: "imported-root",
		IsCA:       true,
		NotBefore:  now.Add(-time.Hour),
		NotAfter:   now.Add(24 * time.Hour),
	})
	rootResponse := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/certificate-assets/paste",
		map[string]any{
			"name": "imported-root",
			"crt":  root.CertPEM,
			"key":  root.KeyPEM,
		},
		http.StatusCreated,
		sessionCookie,
	)
	rootItem := rootResponse.JSON["item"].(map[string]any)
	if rootItem["asset_type"] != "ca" {
		t.Fatalf("expected root import to be detected as ca: %#v", rootItem)
	}

	leaf := issueAPITestCertificate(t, apiTestCertificateSpec{
		CommonName: "leaf.example.com",
		NotBefore:  now.Add(-time.Hour),
		NotAfter:   now.Add(24 * time.Hour),
		Issuer:     &root,
	})
	leafResponse := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/certificate-assets/paste",
		map[string]any{
			"name": "leaf-cert",
			"crt":  leaf.CertPEM,
			"key":  leaf.KeyPEM,
		},
		http.StatusCreated,
		sessionCookie,
	)
	leafItem := leafResponse.JSON["item"].(map[string]any)
	if leafItem["asset_type"] != "certificate" {
		t.Fatalf("expected leaf import to be detected as certificate: %#v", leafItem)
	}
	if _, exists := leafItem["issuer_asset_id"]; exists {
		t.Fatalf("expected uploaded leaf to omit issuer_asset_id: %#v", leafItem)
	}

	intermediate := issueAPITestCertificate(t, apiTestCertificateSpec{
		CommonName: "intermediate-ca",
		IsCA:       true,
		NotBefore:  now.Add(-time.Hour),
		NotAfter:   now.Add(24 * time.Hour),
		Issuer:     &root,
	})
	intermediateResponse := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/certificate-assets/paste",
		map[string]any{
			"name": "intermediate-ca",
			"crt":  intermediate.CertPEM,
			"key":  intermediate.KeyPEM,
		},
		http.StatusCreated,
		sessionCookie,
	)
	intermediateItem := intermediateResponse.JSON["item"].(map[string]any)
	if intermediateItem["asset_type"] != "ca" {
		t.Fatalf("expected intermediate import to be detected as ca: %#v", intermediateItem)
	}
	if _, exists := intermediateItem["issuer_asset_id"]; exists {
		t.Fatalf("expected uploaded intermediate to omit issuer_asset_id: %#v", intermediateItem)
	}
}

func TestCertificateAssetsUploadRejectsDuplicateContent(t *testing.T) {
	store := newTestStore(t)
	manager := newTestAuthManager(t, true)

	server, err := NewServer(
		Options{
			Addr:              "127.0.0.1:7080",
			ReadHeaderTimeout: 5 * time.Second,
			Store:             store,
			Auth:              manager,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test",
	)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	sessionCookie := authenticatedManagementCookie(t, manager)
	ca := issueAPITestCertificate(t, apiTestCertificateSpec{
		CommonName: "uploaded-root",
		IsCA:       true,
		NotBefore:  time.Now().UTC().Add(-time.Hour),
		NotAfter:   time.Now().UTC().Add(24 * time.Hour),
	})

	created := performMultipartRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/certificate-assets/upload",
		map[string]string{
			"name": "uploaded-root-a",
		},
		map[string]string{
			"crt": ca.CertPEM,
			"key": ca.KeyPEM,
		},
		http.StatusCreated,
		sessionCookie,
	)
	createdItem := created.JSON["item"].(map[string]any)
	if createdItem["asset_type"] != "ca" {
		t.Fatalf("expected upload import to be detected as ca: %#v", createdItem)
	}

	duplicate := performMultipartRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/certificate-assets/upload",
		map[string]string{
			"name": "uploaded-root-b",
		},
		map[string]string{
			"crt": ca.CertPEM,
			"key": ca.KeyPEM,
		},
		http.StatusConflict,
		sessionCookie,
	)
	if duplicate.JSON["error_code"] != "certificate_asset_duplicate_content" {
		t.Fatalf("unexpected duplicate response: %#v", duplicate.JSON)
	}
	details := duplicate.JSON["details"].(map[string]any)
	duplicates := details["duplicates"].([]any)
	if len(duplicates) != 1 {
		t.Fatalf("unexpected duplicate details: %#v", duplicate.JSON)
	}
}

func TestCertificateAssetsDeleteCascadeIncludesUploadedDependencyChain(t *testing.T) {
	store := newTestStore(t)
	manager := newTestAuthManager(t, true)

	server, err := NewServer(
		Options{
			Addr:              "127.0.0.1:7080",
			ReadHeaderTimeout: 5 * time.Second,
			Store:             store,
			Auth:              manager,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test",
	)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	sessionCookie := authenticatedManagementCookie(t, manager)
	now := time.Now().UTC()

	root := issueAPITestCertificate(t, apiTestCertificateSpec{
		CommonName: "delete-upload-root",
		IsCA:       true,
		NotBefore:  now.Add(-time.Hour),
		NotAfter:   now.Add(24 * time.Hour),
	})
	rootResponse := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/certificate-assets/paste",
		map[string]any{
			"name": "delete-upload-root",
			"crt":  root.CertPEM,
			"key":  root.KeyPEM,
		},
		http.StatusCreated,
		sessionCookie,
	)
	rootID := int64(rootResponse.JSON["item"].(map[string]any)["id"].(float64))

	intermediate := issueAPITestCertificate(t, apiTestCertificateSpec{
		CommonName: "delete-upload-intermediate",
		IsCA:       true,
		NotBefore:  now.Add(-time.Hour),
		NotAfter:   now.Add(24 * time.Hour),
		Issuer:     &root,
	})
	intermediateResponse := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/certificate-assets/paste",
		map[string]any{
			"name": "delete-upload-intermediate",
			"crt":  intermediate.CertPEM,
			"key":  intermediate.KeyPEM,
		},
		http.StatusCreated,
		sessionCookie,
	)
	intermediateID := int64(intermediateResponse.JSON["item"].(map[string]any)["id"].(float64))

	leaf := issueAPITestCertificate(t, apiTestCertificateSpec{
		CommonName: "delete-upload-leaf",
		NotBefore:  now.Add(-time.Hour),
		NotAfter:   now.Add(24 * time.Hour),
		Issuer:     &intermediate,
	})
	leafResponse := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/certificate-assets/paste",
		map[string]any{
			"name": "delete-upload-leaf",
			"crt":  leaf.CertPEM,
			"key":  leaf.KeyPEM,
		},
		http.StatusCreated,
		sessionCookie,
	)
	leafID := int64(leafResponse.JSON["item"].(map[string]any)["id"].(float64))

	impact := performRequest(
		t,
		server.Handler(),
		http.MethodGet,
		"/api/v1/certificate-assets/"+strconv.FormatInt(rootID, 10)+"/delete-impact",
		nil,
		http.StatusOK,
		sessionCookie,
	)
	if impact.JSON["requires_confirmation"] != true {
		t.Fatalf("expected delete impact confirmation: %#v", impact.JSON)
	}

	affectedItems := impact.JSON["affected_items"].([]any)
	if len(affectedItems) != 2 {
		t.Fatalf("unexpected affected items: %#v", impact.JSON)
	}

	affectedIDs := make(map[int64]struct{}, len(affectedItems))
	for _, raw := range affectedItems {
		item := raw.(map[string]any)
		asset := item["item"].(map[string]any)
		affectedIDs[int64(asset["id"].(float64))] = struct{}{}
	}
	if _, exists := affectedIDs[intermediateID]; !exists {
		t.Fatalf("expected intermediate in delete impact: %#v", impact.JSON)
	}
	if _, exists := affectedIDs[leafID]; !exists {
		t.Fatalf("expected leaf in delete impact: %#v", impact.JSON)
	}

	deleteBlocked := performRequest(
		t,
		server.Handler(),
		http.MethodDelete,
		"/api/v1/certificate-assets/"+strconv.FormatInt(rootID, 10),
		nil,
		http.StatusConflict,
		sessionCookie,
	)
	if deleteBlocked.JSON["error_code"] != "certificate_asset_delete_requires_confirmation" {
		t.Fatalf("unexpected delete blocked payload: %#v", deleteBlocked.JSON)
	}

	deleted := performRequest(
		t,
		server.Handler(),
		http.MethodDelete,
		"/api/v1/certificate-assets/"+strconv.FormatInt(rootID, 10)+"?cascade=true",
		nil,
		http.StatusOK,
		sessionCookie,
	)
	deletedIDs := deleted.JSON["deleted_ids"].([]any)
	if len(deletedIDs) != 3 {
		t.Fatalf("expected root, intermediate and leaf to be deleted: %#v", deleted.JSON)
	}

	listAfterDelete := performRequest(
		t,
		server.Handler(),
		http.MethodGet,
		"/api/v1/certificate-assets",
		nil,
		http.StatusOK,
		sessionCookie,
	)
	if len(listAfterDelete.JSON["items"].([]any)) != 0 {
		t.Fatalf("expected empty asset list after delete: %#v", listAfterDelete.JSON)
	}
}

func TestCertificateAssetDownloadOptionsDifferentiateGeneratedAndUploadedAssets(t *testing.T) {
	store := newTestStore(t)
	manager := newTestAuthManager(t, true)

	server, err := NewServer(
		Options{
			Addr:              "127.0.0.1:7080",
			ReadHeaderTimeout: 5 * time.Second,
			Store:             store,
			Auth:              manager,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test",
	)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	sessionCookie := authenticatedManagementCookie(t, manager)

	rootResponse := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/certificate-assets/generate",
		map[string]any{
			"name":          "generated-root",
			"asset_type":    "ca",
			"common_name":   "Generated Root",
			"validity_days": 365,
		},
		http.StatusCreated,
		sessionCookie,
	)
	rootID := int64(rootResponse.JSON["item"].(map[string]any)["id"].(float64))

	leafResponse := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/certificate-assets/generate",
		map[string]any{
			"name":            "generated-leaf",
			"asset_type":      "certificate",
			"issuer_asset_id": rootID,
			"common_name":     "generated.example.com",
			"validity_days":   30,
		},
		http.StatusCreated,
		sessionCookie,
	)
	leafID := int64(leafResponse.JSON["item"].(map[string]any)["id"].(float64))

	uploadedRoot := issueAPITestCertificate(t, apiTestCertificateSpec{
		CommonName: "uploaded-root-for-download",
		IsCA:       true,
		NotBefore:  time.Now().UTC().Add(-time.Hour),
		NotAfter:   time.Now().UTC().Add(24 * time.Hour),
	})
	uploadResponse := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/certificate-assets/paste",
		map[string]any{
			"name": "uploaded-root",
			"crt":  uploadedRoot.CertPEM,
			"key":  uploadedRoot.KeyPEM,
		},
		http.StatusCreated,
		sessionCookie,
	)
	uploadID := int64(uploadResponse.JSON["item"].(map[string]any)["id"].(float64))

	generatedLeafOptions := performRequest(
		t,
		server.Handler(),
		http.MethodGet,
		"/api/v1/certificate-assets/"+strconv.FormatInt(leafID, 10)+"/download-options",
		nil,
		http.StatusOK,
		sessionCookie,
	)
	generatedLeafModes := generatedLeafOptions.JSON["modes"].([]any)
	if len(generatedLeafModes) != 2 {
		t.Fatalf("unexpected generated leaf download modes: %#v", generatedLeafOptions.JSON)
	}
	generatedLeafChain := generatedLeafOptions.JSON["chain_items"].([]any)
	if len(generatedLeafChain) != 2 {
		t.Fatalf("unexpected generated leaf chain: %#v", generatedLeafOptions.JSON)
	}
	if _, exists := generatedLeafOptions.JSON["tree_items"]; exists {
		t.Fatalf("did not expect generated leaf tree items: %#v", generatedLeafOptions.JSON)
	}

	uploadedOptions := performRequest(
		t,
		server.Handler(),
		http.MethodGet,
		"/api/v1/certificate-assets/"+strconv.FormatInt(uploadID, 10)+"/download-options",
		nil,
		http.StatusOK,
		sessionCookie,
	)
	uploadedModes := uploadedOptions.JSON["modes"].([]any)
	if len(uploadedModes) != 1 {
		t.Fatalf("unexpected uploaded asset download modes: %#v", uploadedOptions.JSON)
	}
	firstMode := uploadedModes[0].(map[string]any)
	if firstMode["mode"] != "original" {
		t.Fatalf("unexpected uploaded asset default mode: %#v", uploadedOptions.JSON)
	}
	if _, exists := uploadedOptions.JSON["chain_items"]; exists {
		t.Fatalf("did not expect uploaded asset chain items: %#v", uploadedOptions.JSON)
	}
}

func TestCertificateAssetDownloadGeneratedLeafSingleAndChain(t *testing.T) {
	store := newTestStore(t)
	manager := newTestAuthManager(t, true)

	server, err := NewServer(
		Options{
			Addr:              "127.0.0.1:7080",
			ReadHeaderTimeout: 5 * time.Second,
			Store:             store,
			Auth:              manager,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test",
	)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	sessionCookie := authenticatedManagementCookie(t, manager)

	rootResponse := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/certificate-assets/generate",
		map[string]any{
			"name":          "generated-root",
			"asset_type":    "ca",
			"common_name":   "Generated Root",
			"validity_days": 365,
		},
		http.StatusCreated,
		sessionCookie,
	)
	rootID := int64(rootResponse.JSON["item"].(map[string]any)["id"].(float64))

	leafResponse := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/certificate-assets/generate",
		map[string]any{
			"name":            "generated-leaf",
			"asset_type":      "certificate",
			"issuer_asset_id": rootID,
			"common_name":     "generated.example.com",
			"validity_days":   30,
			"dns_names":       []string{"generated.example.com"},
		},
		http.StatusCreated,
		sessionCookie,
	)
	leafID := int64(leafResponse.JSON["item"].(map[string]any)["id"].(float64))

	singleDownload := performBinaryRequest(
		t,
		server.Handler(),
		http.MethodGet,
		"/api/v1/certificate-assets/"+strconv.FormatInt(leafID, 10)+"/download",
		http.StatusOK,
		sessionCookie,
	)
	if contentType := singleDownload.Headers.Get("Content-Type"); contentType != "application/zip" {
		t.Fatalf("unexpected single download content type: %q", contentType)
	}
	if !strings.Contains(singleDownload.Headers.Get("Content-Disposition"), ".zip") {
		t.Fatalf("expected zip content disposition, got: %q", singleDownload.Headers.Get("Content-Disposition"))
	}

	singleEntries := readZipEntries(t, singleDownload.Body)
	if len(singleEntries) != 2 {
		t.Fatalf("unexpected single download archive entries: %#v", singleEntries)
	}
	singleCRTName, singleCRT := findZipEntryBySuffix(t, singleEntries, ".crt")
	if strings.Contains(singleCRTName, "-chain.crt") {
		t.Fatalf("single download should not use chain crt name: %q", singleCRTName)
	}
	if countPEMCertificates(singleCRT) != 1 {
		t.Fatalf("expected single download crt to contain one certificate, got %d", countPEMCertificates(singleCRT))
	}
	if _, keyContent := findZipEntryBySuffix(t, singleEntries, ".key"); !strings.Contains(keyContent, "BEGIN PRIVATE KEY") {
		t.Fatalf("expected single download key file, got: %#v", singleEntries)
	}

	chainDownload := performBinaryRequest(
		t,
		server.Handler(),
		http.MethodGet,
		"/api/v1/certificate-assets/"+strconv.FormatInt(leafID, 10)+"/download?mode=chain&ancestor_id="+strconv.FormatInt(rootID, 10),
		http.StatusOK,
		sessionCookie,
	)
	chainEntries := readZipEntries(t, chainDownload.Body)
	if len(chainEntries) != 2 {
		t.Fatalf("unexpected chain download archive entries: %#v", chainEntries)
	}
	chainCRTName, chainCRT := findZipEntryBySuffix(t, chainEntries, ".crt")
	if !strings.Contains(chainCRTName, "-chain.crt") {
		t.Fatalf("expected chain crt name, got %q", chainCRTName)
	}
	if countPEMCertificates(chainCRT) != 2 {
		t.Fatalf("expected chain download crt to contain two certificates, got %d", countPEMCertificates(chainCRT))
	}
	if !strings.HasPrefix(chainCRT, singleCRT) {
		t.Fatalf("expected chain crt to start with leaf certificate")
	}
}

func TestCertificateAssetDownloadGeneratedCATree(t *testing.T) {
	store := newTestStore(t)
	manager := newTestAuthManager(t, true)

	server, err := NewServer(
		Options{
			Addr:              "127.0.0.1:7080",
			ReadHeaderTimeout: 5 * time.Second,
			Store:             store,
			Auth:              manager,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test",
	)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	sessionCookie := authenticatedManagementCookie(t, manager)

	rootResponse := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/certificate-assets/generate",
		map[string]any{
			"name":          "tree-root",
			"asset_type":    "ca",
			"common_name":   "Tree Root",
			"validity_days": 365,
		},
		http.StatusCreated,
		sessionCookie,
	)
	rootID := int64(rootResponse.JSON["item"].(map[string]any)["id"].(float64))

	intermediateResponse := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/certificate-assets/generate",
		map[string]any{
			"name":            "tree-intermediate",
			"asset_type":      "ca",
			"issuer_asset_id": rootID,
			"common_name":     "Tree Intermediate",
			"validity_days":   180,
		},
		http.StatusCreated,
		sessionCookie,
	)
	intermediateID := int64(intermediateResponse.JSON["item"].(map[string]any)["id"].(float64))

	leafResponse := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/certificate-assets/generate",
		map[string]any{
			"name":            "tree-leaf",
			"asset_type":      "certificate",
			"issuer_asset_id": intermediateID,
			"common_name":     "tree.example.com",
			"validity_days":   30,
		},
		http.StatusCreated,
		sessionCookie,
	)
	leafID := int64(leafResponse.JSON["item"].(map[string]any)["id"].(float64))

	treeDownload := performBinaryRequest(
		t,
		server.Handler(),
		http.MethodGet,
		"/api/v1/certificate-assets/"+strconv.FormatInt(rootID, 10)+"/download?mode=tree",
		http.StatusOK,
		sessionCookie,
	)
	treeEntries := readZipEntries(t, treeDownload.Body)
	if len(treeEntries) != 6 {
		t.Fatalf("unexpected tree download archive entries: %#v", treeEntries)
	}
	if _, content := findZipEntryByName(t, treeEntries, expectedDownloadFileName("tree-root", rootID, ".crt")); countPEMCertificates(content) != 1 {
		t.Fatalf("expected root crt in tree download")
	}
	if _, content := findZipEntryByName(t, treeEntries, expectedDownloadFileName("tree-intermediate", intermediateID, ".crt")); countPEMCertificates(content) != 1 {
		t.Fatalf("expected intermediate crt in tree download")
	}
	if _, content := findZipEntryByName(t, treeEntries, expectedDownloadFileName("tree-leaf", leafID, ".crt")); countPEMCertificates(content) != 1 {
		t.Fatalf("expected leaf crt in tree download")
	}
}

func TestCertificateAssetDownloadUploadedAssetOriginalOnly(t *testing.T) {
	store := newTestStore(t)
	manager := newTestAuthManager(t, true)

	server, err := NewServer(
		Options{
			Addr:              "127.0.0.1:7080",
			ReadHeaderTimeout: 5 * time.Second,
			Store:             store,
			Auth:              manager,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test",
	)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	sessionCookie := authenticatedManagementCookie(t, manager)
	now := time.Now().UTC()

	root := issueAPITestCertificate(t, apiTestCertificateSpec{
		CommonName: "uploaded-download-root",
		IsCA:       true,
		NotBefore:  now.Add(-time.Hour),
		NotAfter:   now.Add(24 * time.Hour),
	})
	rootResponse := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/certificate-assets/paste",
		map[string]any{
			"name": "uploaded-download-root",
			"crt":  root.CertPEM,
			"key":  root.KeyPEM,
		},
		http.StatusCreated,
		sessionCookie,
	)
	if rootResponse.JSON["item"].(map[string]any)["asset_type"] != "ca" {
		t.Fatalf("expected uploaded root ca: %#v", rootResponse.JSON)
	}

	leaf := issueAPITestCertificate(t, apiTestCertificateSpec{
		CommonName: "uploaded-download-leaf",
		NotBefore:  now.Add(-time.Hour),
		NotAfter:   now.Add(24 * time.Hour),
		Issuer:     &root,
	})
	originalCRT := leaf.CertPEM + root.CertPEM
	leafResponse := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/certificate-assets/paste",
		map[string]any{
			"name": "uploaded-download-leaf",
			"crt":  originalCRT,
			"key":  leaf.KeyPEM,
		},
		http.StatusCreated,
		sessionCookie,
	)
	leafID := int64(leafResponse.JSON["item"].(map[string]any)["id"].(float64))

	originalDownload := performBinaryRequest(
		t,
		server.Handler(),
		http.MethodGet,
		"/api/v1/certificate-assets/"+strconv.FormatInt(leafID, 10)+"/download",
		http.StatusOK,
		sessionCookie,
	)
	originalEntries := readZipEntries(t, originalDownload.Body)
	if len(originalEntries) != 2 {
		t.Fatalf("unexpected original download archive entries: %#v", originalEntries)
	}
	_, originalCRTContent := findZipEntryBySuffix(t, originalEntries, ".crt")
	if originalCRTContent != originalCRT {
		t.Fatalf("expected uploaded asset crt to preserve original structure")
	}
	if _, keyContent := findZipEntryBySuffix(t, originalEntries, ".key"); !strings.Contains(keyContent, "BEGIN PRIVATE KEY") {
		t.Fatalf("expected uploaded asset key in archive: %#v", originalEntries)
	}

	rejected := performRequest(
		t,
		server.Handler(),
		http.MethodGet,
		"/api/v1/certificate-assets/"+strconv.FormatInt(leafID, 10)+"/download?mode=chain",
		nil,
		http.StatusUnprocessableEntity,
		sessionCookie,
	)
	if rejected.JSON["error_code"] != "certificate_asset_validation_failed" {
		t.Fatalf("unexpected invalid upload download response: %#v", rejected.JSON)
	}
}

func performMultipartRequest(
	t *testing.T,
	handler http.Handler,
	method string,
	target string,
	fields map[string]string,
	files map[string]string,
	wantStatus int,
	cookies ...*http.Cookie,
) testResponse {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatalf("write multipart field: %v", err)
		}
	}
	for field, value := range files {
		part, err := writer.CreateFormFile(field, field+".pem")
		if err != nil {
			t.Fatalf("create multipart file: %v", err)
		}
		if _, err := io.Copy(part, bytes.NewBufferString(value)); err != nil {
			t.Fatalf("write multipart file: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	request := httptest.NewRequest(method, target, &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != wantStatus {
		t.Fatalf("unexpected status for %s %s: got %d want %d body=%s", method, target, recorder.Code, wantStatus, recorder.Body.String())
	}

	response := testResponse{
		JSON:    map[string]any{},
		Cookies: recorder.Result().Cookies(),
	}
	if recorder.Body.Len() == 0 {
		return response
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response.JSON); err != nil {
		t.Fatalf("decode multipart response: %v", err)
	}
	return response
}

type binaryTestResponse struct {
	Body    []byte
	Headers http.Header
	Cookies []*http.Cookie
}

func performBinaryRequest(
	t *testing.T,
	handler http.Handler,
	method string,
	target string,
	wantStatus int,
	cookies ...*http.Cookie,
) binaryTestResponse {
	t.Helper()

	request := httptest.NewRequest(method, target, nil)
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != wantStatus {
		t.Fatalf("unexpected status for %s %s: got %d want %d body=%s", method, target, recorder.Code, wantStatus, recorder.Body.String())
	}

	return binaryTestResponse{
		Body:    append([]byte(nil), recorder.Body.Bytes()...),
		Headers: recorder.Result().Header.Clone(),
		Cookies: recorder.Result().Cookies(),
	}
}

func readZipEntries(t *testing.T, body []byte) map[string]string {
	t.Helper()

	reader, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("open zip archive: %v", err)
	}

	items := make(map[string]string, len(reader.File))
	for _, file := range reader.File {
		stream, err := file.Open()
		if err != nil {
			t.Fatalf("open zip entry %q: %v", file.Name, err)
		}
		raw, err := io.ReadAll(stream)
		_ = stream.Close()
		if err != nil {
			t.Fatalf("read zip entry %q: %v", file.Name, err)
		}
		items[file.Name] = string(raw)
	}
	return items
}

func findZipEntryBySuffix(t *testing.T, entries map[string]string, suffix string) (string, string) {
	t.Helper()

	for name, content := range entries {
		if strings.HasSuffix(name, suffix) {
			return name, content
		}
	}
	t.Fatalf("missing zip entry with suffix %q in %#v", suffix, entries)
	return "", ""
}

func findZipEntryByName(t *testing.T, entries map[string]string, name string) (string, string) {
	t.Helper()

	content, ok := entries[name]
	if !ok {
		t.Fatalf("missing zip entry %q in %#v", name, entries)
	}
	return name, content
}

func countPEMCertificates(value string) int {
	return strings.Count(value, "BEGIN CERTIFICATE")
}

func expectedDownloadFileName(name string, id int64, extension string) string {
	return sanitizeDownloadTestName(name, id) + extension
}

func sanitizeDownloadTestName(name string, id int64) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		name = "certificate"
	}

	var builder strings.Builder
	lastDash := false
	for _, char := range name {
		switch {
		case char >= 'a' && char <= 'z':
			builder.WriteRune(char)
			lastDash = false
		case char >= '0' && char <= '9':
			builder.WriteRune(char)
			lastDash = false
		case char == '-' || char == '_':
			builder.WriteRune(char)
			lastDash = false
		default:
			if !lastDash {
				builder.WriteByte('-')
				lastDash = true
			}
		}
	}

	sanitized := strings.Trim(builder.String(), "-_.")
	if sanitized == "" {
		sanitized = "certificate"
	}
	return sanitized + "-" + strconv.FormatInt(id, 10)
}

func parseSingleCertificatePEM(t *testing.T, value string) *x509.Certificate {
	t.Helper()

	block, rest := pem.Decode([]byte(value))
	if block == nil {
		t.Fatalf("expected certificate pem block")
	}
	if block.Type != "CERTIFICATE" {
		t.Fatalf("unexpected pem block type %q", block.Type)
	}
	if len(bytes.TrimSpace(rest)) > 0 {
		t.Fatalf("expected a single certificate pem block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse certificate pem: %v", err)
	}
	return cert
}

func parsePKCS8PrivateKeyPEM(t *testing.T, value string) any {
	t.Helper()

	block, rest := pem.Decode([]byte(value))
	if block == nil {
		t.Fatalf("expected private key pem block")
	}
	if block.Type != "PRIVATE KEY" {
		t.Fatalf("unexpected private key pem block type %q", block.Type)
	}
	if len(bytes.TrimSpace(rest)) > 0 {
		t.Fatalf("expected a single private key pem block")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("parse private key pem: %v", err)
	}
	return key
}

type apiTestCertificateSpec struct {
	CommonName string
	IsCA       bool
	NotBefore  time.Time
	NotAfter   time.Time
	Issuer     *apiIssuedCertificate
}

type apiIssuedCertificate struct {
	CertPEM string
	KeyPEM  string
	Cert    *x509.Certificate
	Key     *ecdsa.PrivateKey
}

func issueAPITestCertificate(t *testing.T, spec apiTestCertificateSpec) apiIssuedCertificate {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject: pkix.Name{
			CommonName: spec.CommonName,
		},
		BasicConstraintsValid: true,
		IsCA:                  spec.IsCA,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		NotBefore:             spec.NotBefore,
		NotAfter:              spec.NotAfter,
	}
	if spec.IsCA {
		template.KeyUsage = x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign | x509.KeyUsageCRLSign
	}

	parent := template
	signerKey := key
	if spec.Issuer != nil {
		parent = spec.Issuer.Cert
		signerKey = spec.Issuer.Key
	}

	der, err := x509.CreateCertificate(rand.Reader, template, parent, key.Public(), signerKey)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}

	return apiIssuedCertificate{
		CertPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		KeyPEM:  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})),
		Cert:    parsed,
		Key:     key,
	}
}
