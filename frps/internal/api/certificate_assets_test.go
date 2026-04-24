package api

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
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
	"testing"
	"time"
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
			"name":       "bad-cert",
			"asset_type": "certificate",
			"crt":        "not a pem",
			"key":        "not a key",
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

	performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/certificate-assets/paste",
		map[string]any{
			"name":       "uploaded-root-a",
			"asset_type": "ca",
			"crt":        ca.CertPEM,
			"key":        ca.KeyPEM,
		},
		http.StatusCreated,
		sessionCookie,
	)

	duplicate := performMultipartRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/certificate-assets/upload",
		map[string]string{
			"name":       "uploaded-root-b",
			"asset_type": "ca",
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
