# DWS network dependency audit

Reviewed 2026-09-09 against the installed open edition DWS v1.0.62-beta.6,
commit `e45f7ca904b1cfd9974c23e09d15b42e39836f96` of
[DingTalk-Real-AI/dingtalk-workspace-cli](https://github.com/DingTalk-Real-AI/dingtalk-workspace-cli/tree/e45f7ca904b1cfd9974c23e09d15b42e39836f96).
The review covered production Go URL literals, bare host names, network call
sites, endpoint rewrites, direct transfers, Stream, plugins/upgrades and the
shipped file-transfer documentation. Test placeholders and links in licenses
are not service dependencies.

## Failure mechanism

MCP authentication and upload initialization can succeed while the subsequent
PUT fails. `internal/helpers/chat_media_upload.go` and
`internal/shortcut/chat/unified_send.go` orchestrate init -> direct upload ->
commit. `internal/localio/upload.go` sends the file to the signed URL returned
by the authenticated service. That URL's hostname is a separate network
dependency from mcp-gw.dingtalk.com. The reported failed destination was
sh-dualstack.trans.dingtalk.com; trans.dingtalk.com and down.dingtalk.com were
also absent from the previous defaults. A DNS error alone does not establish
an upstream DNS outage; ASB deny rules can produce the same symptom.

## Default rules and sources

| Flow | Allowed destinations | Evidence in the pinned DWS source |
| --- | --- | --- |
| OAuth, MCP, API, developer portal | Existing .com API/MCP/portal hosts; login/pre-login .com; login, api, mcp, mcp-gw, open-dev .io and pre login/MCP/gateway/portal variants | internal/auth/endpoints.go; pkg/config/constants.go; internal/app/direct_runtime.go; internal/transport/client.go; internal/syncdata/endpoints.go |
| Chat file/media transfer | trans.dingtalk.com; sh-dualstack.trans.dingtalk.com; *.trans.dingtalk.com; down.dingtalk.com; *.down.dingtalk.com; upload.dingtalk.com; download.dingtalk.com | Incident destination; helpers/chat.go; helpers/chat_media_upload.go; localio/upload.go; localio upload/download contract tests |
| Document attachments, images, drive and sheet transfers/exports | alidocs.oss-cn-zhangjiakou.aliyuncs.com; alidocs2.oss-cn-zhangjiakou.aliyuncs.com | helpers/doc.go, doc_style.go, drive.go, drive_export.go, sheet_export.go, sheet_float_image_file.go; localio/download_test.go; skills/mono/references/products/doc/doc-read.md |
| Enterprise/personal mail attachment POST and download | alimail-cn.aliyuncs.com; alimail-personal.aliyuncs.com | helpers/mail.go: mailUploadBaseURL, httpPutMailAttachment, mail download helpers |
| Personal events and bot Stream | wss-open-connection.dingtalk.com; pre-wss-open-connection.dingtalk.com; existing api.dingtalk.com | event/source/dingtalk.go, portal_ticket.go and dingtalk_test.go; official Stream FAQ linked below |
| Document/product URL handling and marketplace | alidocs.dingtalk.com; docs.dingtalk.com; shanji.dingtalk.com; aihub.dingtalk.com; open.dingtalk.com; s.dingtalk.com; img.alicdn.com | helpers/doc.go, aitable.go; app/skill_command.go, runner.go; auth/oauth_helpers.go; shipped minutes/url-patterns docs |
| Encrypted SafeChat | server.safeding.com | internal/msgcrypto/session.go; app/safechat_command.go |
| Official distribution and GitHub plugin source | github.com; api.github.com; raw.githubusercontent.com; objects.githubusercontent.com; release-assets.githubusercontent.com; gosspublic.alicdn.com | internal/upgrade/github.go; internal/plugin/loader.go; scripts/release/sync-to-oss.sh; GitHub release downloads can redirect to asset hosts |
| Built-in Gemini API connector | generativelanguage.googleapis.com | internal/helpers/connect_gemini_api.go |

The Stream hosts are also documented in the official
[DingTalk Stream network FAQ](https://opensource.dingtalk.com/developerpedia/docs/learn/stream/faq/).

## Dynamic-address boundary

Source review cannot enumerate every possible DWS destination. Download and
export URLs are issued at runtime. DWS intentionally supports custom enterprise
storage hosts; its download tests include a dedicated enterprise storage host,
and plugin repositories, MCP servers, audit collectors and mirrors can be
user-configured. Those destinations must remain exact Runtime entries or
configured Agent/service URL dependencies. Shared public OSS domains are not
blanket-allowed: arbitrary tenants can own buckets there.

The built-in wildcard families include `*.trans.dingtalk.com` and
`*.down.dingtalk.com`. On 2026-09-15, the user additionally requested default
access to `*.alibaba-inc.com` and `*.dingtalk.com`; these are accepted by the
creation validator as explicit built-in constants. Apex hosts remain separate
rules. Custom Runtime, Agent and deployment entries still reject wildcards,
URLs and CIDRs. Other broad families such as `*.aliyuncs.com`, and lookalikes
such as `*.dingtalk.com.evil.example`, remain rejected. Default action is deny.

Validation includes creation-boundary regression tests (including rejection of
unlisted/lookalike wildcards), fresh ASB tasks, required-host DNS checks, an actual
DWS upload/download with a synthetic test file and SHA256 comparison, and an
unlisted public-domain negative control. No DingTalk messages are sent.
Policy fingerprints replace old sandboxes at the next task launch; active tasks
finish with their existing policy. Pre-release validation does not deploy or
close the production security finding.
