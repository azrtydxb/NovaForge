package edge

import (
	"fmt"
	"sort"
	"strings"
)

// OpenAPIDocument renders the contract from the same route table the router is
// built from, so the published spec cannot silently drift from what is served.
// The coverage test still compares the two, which catches a hand edit of the
// checked-in file.
func OpenAPIDocument() string {
	var b strings.Builder
	b.WriteString(`openapi: 3.1.0
info:
  title: NovaForge API
  version: 0.1.0
  description: >-
    The NovaForge control plane. The web GUI is deferred, so this contract is
    the complete description of the platform for every client: the nf CLI,
    external agents, and the future GUI.
servers:
  - url: /
security:
  - bearerAuth: []
components:
  securitySchemes:
    bearerAuth:
      type: http
      scheme: bearer
  responses:
    Unauthorized:
      description: No usable credentials were presented
    Forbidden:
      description: The caller's organization scope does not permit this
    NotFound:
      description: No such resource in the caller's organization
paths:
`)
	byPath := map[string][]Route{}
	for _, r := range Routes() {
		if r.Pattern == "/healthz" {
			continue
		}
		p := openAPIPath(r.Pattern)
		byPath[p] = append(byPath[p], r)
	}
	paths := make([]string, 0, len(byPath))
	for p := range byPath {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	for _, p := range paths {
		fmt.Fprintf(&b, "  %s:\n", p)
		rs := byPath[p]
		sort.Slice(rs, func(i, j int) bool { return rs[i].Method < rs[j].Method })
		for _, r := range rs {
			fmt.Fprintf(&b, "    %s:\n", strings.ToLower(r.Method))
			fmt.Fprintf(&b, "      operationId: %s\n", r.OpID)
			fmt.Fprintf(&b, "      summary: %s\n", r.Summary)
			if params := pathParams(p); len(params) > 0 {
				b.WriteString("      parameters:\n")
				for _, name := range params {
					fmt.Fprintf(&b, "        - name: %s\n          in: path\n          required: true\n          schema:\n            type: string\n", name)
				}
			}
			b.WriteString("      responses:\n")
			b.WriteString("        \"200\":\n          description: Success\n")
			b.WriteString("        \"401\":\n          $ref: \"#/components/responses/Unauthorized\"\n")
			b.WriteString("        \"403\":\n          $ref: \"#/components/responses/Forbidden\"\n")
			b.WriteString("        \"404\":\n          $ref: \"#/components/responses/NotFound\"\n")
		}
	}
	return b.String()
}

func openAPIPath(p string) string { return strings.ReplaceAll(p, "/*", "/{path}") }

func pathParams(p string) []string {
	var out []string
	for _, seg := range strings.Split(p, "/") {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			out = append(out, strings.Trim(seg, "{}"))
		}
	}
	return out
}
