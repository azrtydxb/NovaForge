package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// The audit log must show what a tool call actually did, which means its
// arguments and not merely that some arguments were present. It used to keep
// only their length, so "the agent wrote a file" was recorded while which file
// was not — an audit that cannot answer the first question anyone asks of it.
//
// Arguments are not uniformly safe to keep, though: a file's contents or a
// comment's body can carry anything the agent was handed, including a secret it
// read. So each argument is either an identifier, which names what the call
// acted on and is kept verbatim, or content, which is kept as its length and
// digest. The digest still makes the evidence checkable — the same bytes
// produce the same digest — without the audit log becoming a copy of them.
//
// Identifiers are the default because they are what the built-in tools take:
// repository names, refs, paths, symbols, work item ids. Content is declared,
// per tool and per argument, below.
var contentArgs = map[string]map[string]bool{
	"workspace.write_file": {"content": true},
	"work.comment":         {"body": true},
	"knowledge.record":     {"body": true},
}

// maxAuditArgBytes bounds a kept identifier. One well past any real repository
// path is digested instead, so a caller cannot grow the audit log by passing a
// megabyte where a branch name belongs.
const maxAuditArgBytes = 512

// digestOf reduces a value to a length and a digest: enough to prove later
// which bytes were passed, without retaining them.
func digestOf(raw []byte) map[string]any {
	sum := sha256.Sum256(raw)
	return map[string]any{"bytes": len(raw), "sha256": hex.EncodeToString(sum[:])}
}

// AuditArgs reduces a call's arguments to the representation the audit log
// keeps. builtin says whether this tool's arguments are the ones described in
// Specs: an external MCP server's are not, so nothing is assumed about which of
// them are safe to keep and every one is digested.
func AuditArgs(tool string, builtin bool, argsJSON []byte) []byte {
	var fields map[string]json.RawMessage
	if len(argsJSON) == 0 || json.Unmarshal(argsJSON, &fields) != nil {
		// Arguments that are not a JSON object are not described by any schema,
		// so they are recorded as the shape they were, exactly as before.
		out, _ := json.Marshal(map[string]any{"argument_bytes": len(argsJSON), "valid_json": json.Valid(argsJSON)})
		return out
	}
	content := contentArgs[tool]
	reduced := make(map[string]any, len(fields))
	for name, raw := range fields {
		switch {
		case !builtin, content[name], len(raw) > maxAuditArgBytes:
			reduced[name] = digestOf(raw)
		default:
			reduced[name] = raw
		}
	}
	out, err := json.Marshal(reduced)
	if err != nil {
		return digestJSON(argsJSON)
	}
	return out
}

func digestJSON(argsJSON []byte) []byte {
	out, _ := json.Marshal(digestOf(argsJSON))
	return out
}
