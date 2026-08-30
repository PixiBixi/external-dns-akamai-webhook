/*
Copyright 2026 Jeremy Delgado.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package server

import (
	"net/http"
	"strings"

	"sigs.k8s.io/external-dns/provider/webhook/api"
)

const (
	acceptHeader = "Accept"
	// mediaTypeBase is api.MediaTypeFormatAndVersion without its version parameter.
	mediaTypeBase = "application/external.dns.webhook+json"
	// supportedVersion is the webhook protocol version this provider speaks.
	supportedVersion = "1"
)

// checkAccept guards the GET routes, checkContentType the POST ones. The split
// follows what the ExternalDNS client actually sends: Accept on the reads,
// Content-Type on the writes, and no Accept at all on POST /records. Requiring
// both would reject a conformant client.
func checkAccept(w http.ResponseWriter, req *http.Request) bool {
	return checkMediaType(w, req.Header.Get(acceptHeader), acceptHeader, true)
}

func checkContentType(w http.ResponseWriter, req *http.Request) bool {
	return checkMediaType(w, req.Header.Get(api.ContentTypeHeader), api.ContentTypeHeader, false)
}

// checkMediaType answers the client itself and reports false when the request
// cannot be served: 406 when the header is missing, 415 when it names something
// this provider does not speak.
func checkMediaType(w http.ResponseWriter, value, headerName string, allowWildcard bool) bool {
	if value == "" {
		writeError(w, http.StatusNotAcceptable, "missing "+headerName+" header")
		return false
	}

	ok, version := negotiate(value, allowWildcard)
	if ok {
		return true
	}

	msg := "unsupported media type in " + headerName + ": " + value
	if version != "" {
		msg = "unsupported webhook API version " + version + ": this provider speaks version " + supportedVersion
	}
	writeError(w, http.StatusUnsupportedMediaType, msg)

	return false
}

// negotiate reports whether the header names the webhook media type at a version
// this provider speaks. When the media type matches but the version does not, the
// offending version is returned so the caller can say so.
//
// allowWildcard belongs to Accept only: "*/*" means the client takes whatever we
// send, whereas a body labelled "*/*" names nothing and is not something to parse.
func negotiate(header string, allowWildcard bool) (ok bool, unsupportedVersion string) {
	for entry := range strings.SplitSeq(header, ",") {
		mediaType, params, _ := strings.Cut(strings.TrimSpace(entry), ";")
		switch strings.TrimSpace(mediaType) {
		case mediaTypeBase:
		case "*/*", "application/*":
			if allowWildcard {
				return true, ""
			}
			continue
		default:
			continue
		}

		version := ""
		for param := range strings.SplitSeq(params, ";") {
			if k, v, found := strings.Cut(strings.TrimSpace(param), "="); found && strings.TrimSpace(k) == "version" {
				version = strings.Trim(strings.TrimSpace(v), `"`)
			}
		}
		// An unversioned media type is treated as the version we speak, which is
		// what the protocol has meant since there has only ever been one.
		if version == "" || version == supportedVersion {
			return true, ""
		}
		unsupportedVersion = version
	}

	return false, unsupportedVersion
}
