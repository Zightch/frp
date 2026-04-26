package certassets

import (
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/zightch/frp/frps/internal/api/httpx"
	domaincertassets "github.com/zightch/frp/frps/internal/certassets"
)

func parseBoolQueryValue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func parseDownloadRequest(request *http.Request) (domaincertassets.DownloadRequest, error) {
	query := request.URL.Query()
	item := domaincertassets.DownloadRequest{
		Mode: domaincertassets.DownloadMode(strings.ToLower(strings.TrimSpace(query.Get("mode")))),
	}

	if raw := strings.TrimSpace(query.Get("ancestor_id")); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			return domaincertassets.DownloadRequest{}, &httpx.Error{Status: http.StatusBadRequest, Message: "ancestor_id must be a positive integer"}
		}
		item.AncestorAssetID = &id
	}

	assetIDs, err := parseDownloadAssetIDs(query["asset_ids"])
	if err != nil {
		return domaincertassets.DownloadRequest{}, err
	}
	item.AssetIDs = assetIDs

	return item, nil
}

func parseDownloadAssetIDs(values []string) ([]int64, error) {
	if len(values) == 0 {
		return nil, nil
	}

	seen := make(map[int64]struct{})
	items := make([]int64, 0)
	for _, raw := range values {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}

			id, err := strconv.ParseInt(part, 10, 64)
			if err != nil || id <= 0 {
				return nil, &httpx.Error{Status: http.StatusBadRequest, Message: "asset_ids must contain positive integers"}
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			items = append(items, id)
		}
	}
	if len(items) == 0 {
		return nil, nil
	}
	return items, nil
}

func writeDownload(writer http.ResponseWriter, artifact domaincertassets.DownloadArtifact) {
	writer.Header().Set("Content-Type", artifact.ContentType)
	if contentDisposition := mime.FormatMediaType("attachment", map[string]string{"filename": artifact.FileName}); contentDisposition != "" {
		writer.Header().Set("Content-Disposition", contentDisposition)
	} else {
		writer.Header().Set("Content-Disposition", `attachment; filename="download.zip"`)
	}
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(artifact.Body)
}
