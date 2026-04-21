package openaiwire

import (
	"encoding/json"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// MustJSON marshals a raw-message map for json.Unmarshal into a typed struct.
// It panics if marshaling fails (same contract as the legacy frontend decoders).
func MustJSON(blk map[string]json.RawMessage) []byte {
	b, err := json.Marshal(blk)
	if err != nil {
		panic("openaiwire.MustJSON: " + err.Error())
	}
	return b
}

// ImagePartFromURL maps a URL or data-URL string to a canonical image part.
func ImagePartFromURL(imageURL string) (lipapi.Part, error) {
	p := lipapi.Part{Kind: lipapi.PartImageRef, ImageRef: imageURL}
	if strings.HasPrefix(imageURL, "data:") {
		rest := strings.TrimPrefix(imageURL, "data:")
		semi := strings.Index(rest, ";")
		if semi > 0 {
			p.ImageMIME = rest[:semi]
		}
	}
	return p, nil
}

// FilePartFromBase64 builds a data-URL file part from base64 payload and optional filename.
func FilePartFromBase64(filename, fileData string) lipapi.Part {
	mime := "application/octet-stream"
	low := strings.ToLower(strings.TrimSpace(filename))
	if strings.HasSuffix(low, ".pdf") {
		mime = "application/pdf"
	}
	ref := "data:" + mime + ";base64," + fileData
	return lipapi.FilePart(ref, mime, filename)
}
