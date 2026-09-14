package tools

import "encoding/json"

// Spec is what a model is told about one tool: what it does, and exactly
// what arguments it takes.
//
// Every tool used to be offered to the model under the same generic schema
// ({"type":"object","additionalProperties":true}) with its own name as its
// description. The model had no way to know that git.commit's "files" is an
// object mapping path to content, so it sent a string, every time, and
// every commit failed with an unmarshal error. An agent that cannot call
// its tools is indistinguishable from an agent with nothing to do.
//
// Schemas are written out here rather than derived from the argument
// structs: this is the contract the model reads, and a contract worth
// stating is worth stating plainly.
type Spec struct {
	Description string
	Schema      json.RawMessage
}

// obj builds an object schema from a raw properties body and a required
// list, so each entry below reads as the shape it describes.
func obj(properties, required string) json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{` + properties +
		`},"required":[` + required + `],"additionalProperties":false}`)
}

// Specs describes every built-in tool. A tool absent from this map is still
// callable — the registry, not this map, decides what exists — but the
// model sees only a bare name for it, so anything registered should be
// described here too. ToolSpecsCoverEveryTool pins that.
var Specs = map[string]Spec{
	"repo.search": {
		Description: "Search a repository's indexed code for a query string. Returns matching file paths with the line each match starts at.",
		Schema: obj(
			`"repo":{"type":"string","description":"repository id or name"},`+
				`"query":{"type":"string","description":"text to search for"}`,
			`"repo","query"`),
	},
	"repo.read_file": {
		Description: "Read one file from a repository at a given ref. Use this before editing a file, so an edit is based on what is actually there.",
		Schema: obj(
			`"repo":{"type":"string","description":"repository id or name"},`+
				`"ref":{"type":"string","description":"branch, tag or commit; the default branch if omitted"},`+
				`"path":{"type":"string","description":"path within the repository, e.g. \"README.md\""}`,
			`"repo","path"`),
	},
	"repo.get_symbol": {
		Description: "Look up where a symbol (function, type, constant) is defined, from the engineering graph.",
		Schema: obj(
			`"repo":{"type":"string"},"ref":{"type":"string"},`+
				`"symbol":{"type":"string","description":"the symbol's name"}`,
			`"repo","symbol"`),
	},
	"repo.get_dependencies": {
		Description: "List what a file or symbol depends on, from the engineering graph.",
		Schema: obj(
			`"repo":{"type":"string"},"ref":{"type":"string"},`+
				`"path":{"type":"string","description":"file path or symbol name"}`,
			`"repo","path"`),
	},
	"architecture.query": {
		Description: "Ask the engineering graph an architectural question, such as which services depend on a schema.",
		Schema:      obj(`"query":{"type":"string"}`, `"query"`),
	},
	"workspace.write_file": {
		Description: "Write a file into this run's workspace, which starts as a copy of the repository's default branch. Writing here changes nothing in the repository by itself: git.commit with no \"files\" commits every file you staged this way.",
		Schema: obj(
			`"path":{"type":"string","description":"path relative to the repository root; it may not escape it"},`+
				`"content":{"type":"string","description":"the file's complete new contents"}`,
			`"path","content"`),
	},
	"workspace.read_file": {
		Description: "Read a file from this run's workspace, including files you have written there.",
		Schema:      obj(`"path":{"type":"string","description":"path relative to the repository root"}`, `"path"`),
	},
	"workspace.run": {
		Description: "Run a shell command in this run's workspace (the repository root), e.g. \"go test ./...\". The workspace has no network access. Returns the exit code and the end of the combined output. Use it to check your changes before committing.",
		Schema:      obj(`"command":{"type":"string","description":"the command, run with sh -c"}`, `"command"`),
	},
	"git.diff": {
		Description: "Show the unified diff between two refs of a repository.",
		Schema: obj(
			`"repo":{"type":"string"},`+
				`"from":{"type":"string","description":"the base ref"},`+
				`"to":{"type":"string","description":"the ref to compare against the base"}`,
			`"repo","from","to"`),
	},
	"git.commit": {
		Description: "Commit to a branch. Either give \"files\", an object mapping each path to that file's complete new contents (not a list, not a string), or omit it to commit every file you staged with workspace.write_file. You may write only to the branch your run was granted.",
		Schema: obj(
			`"repo":{"type":"string","description":"repository id or name"},`+
				`"branch":{"type":"string","description":"the branch to commit to; must be the one this run was granted"},`+
				`"message":{"type":"string","description":"the commit message"},`+
				`"files":{"type":"object","description":"path -> complete file contents, e.g. {\"README.md\":\"# Title\\n\"}; omit to commit the staged workspace files","additionalProperties":{"type":"string"}}`,
			`"repo","branch","message"`),
	},
	"ci.run_test": {
		Description: "Run a test suite in CI against a ref.",
		Schema: obj(
			`"repo":{"type":"string"},"ref":{"type":"string"},`+
				`"suite":{"type":"string","description":"the suite to run"}`,
			`"repo","ref"`),
	},
	"ci.get_logs": {
		Description: "Read one CI job's log.",
		Schema:      obj(`"job_id":{"type":"string"}`, `"job_id"`),
	},
	"work.get": {
		Description: "Read the Work Item this run is for: its goal, acceptance criteria and state. Call this first.",
		Schema: obj(
			`"work_item_id":{"type":"string","description":"the work item's id, given to you in the opening brief"}`,
			`"work_item_id"`),
	},
	"work.comment": {
		Description: "Record a comment on a Work Item — what you decided and why. Comments are part of the item's history, not a side channel.",
		Schema: obj(
			`"work_item_id":{"type":"string"},"body":{"type":"string"}`,
			`"work_item_id","body"`),
	},
	"gate.status": {
		Description: "Report the gate outcomes recorded for an Engineering Run.",
		Schema:      obj(`"run_id":{"type":"string"}`, `"run_id"`),
	},
}

// SpecFor returns the description and schema for name. A tool with no entry
// gets its own name as its description and an open object schema, which is
// what every tool used to get.
func SpecFor(name string) Spec {
	if s, ok := Specs[name]; ok {
		return s
	}
	return Spec{
		Description: name,
		Schema:      json.RawMessage(`{"type":"object","additionalProperties":true}`),
	}
}
