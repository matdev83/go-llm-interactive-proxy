package openaiwire

import (
	"encoding/json"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func MarshalBlock(blk map[string]json.RawMessage) ([]byte, error) {
	return json.Marshal(blk)
}

// ImagePartFromURL maps a URL or data-URL string to a canonical image part.
func ImagePartFromURL(imageURL string) (lipapi.Part, error) {
	p := lipapi.Part{Kind: lipapi.PartImageRef, ImageRef: imageURL}
	if rest, ok := strings.CutPrefix(imageURL, "data:"); ok {
		if mimePart, _, found := strings.Cut(rest, ";"); found && mimePart != "" {
			p.ImageMIME = mimePart
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
