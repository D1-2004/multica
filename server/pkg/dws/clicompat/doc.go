// Package clicompat reproduces, in process, the request arguments and the JSON
// output of a few dws CLI (dingtalk-workspace-cli) commands, so a host that
// used to spawn dws can call the DWS MCP gateway directly (see dws.CallRaw)
// and keep feeding its existing parsers the same bytes.
//
// Covered commands (dws Version):
//
//	chat message list --group --time --direction --limit --format json
//	chat data-auth cross-org --all --agentCode wukong --grant-type timed --ttl 7d --yes
//	chat message send (--conversation-id [--at-open-dingtalk-ids] | --open-dingtalk-id)
//	chat +messages-reply --group --message-id --yes
//	chat message query-send-status --open-task-id
//	chat message list-by-ids --msg-ids
//	chat +messages-send --as user --msg-type a2ui --yes
//	chat message update-a2ui-card
//
// A command is reproduced as: build the tool arguments (…Args, which already
// apply dws's local validation), call the tool with dws.CallRaw, map a call
// error with CallFailure, run the payload through CheckPayload, then render the
// success output with the command's …Output function or the failure with
// Failure.LegacyJSON (stderr) / Failure.UnifiedJSON (stdout, chat message send
// only). Every output and error rendering ends with a newline, like dws.
//
// The behaviour is dws's own code: whole dws packages are copied into
// internal/upstream and chatmsg (only import paths rewritten), declarations
// from dws files that import cobra are copied verbatim into dws_helpers.go,
// dws_app.go, dws_chat.go and internal/upstream/output/excerpts.go, and the
// glue here copies the relevant statements of dws's command functions, each
// citing its dws file and line. See NOTICE for the complete list.
package clicompat

// Version is the dws release whose behaviour this package reproduces.
const Version = "v1.0.62-beta.8"
