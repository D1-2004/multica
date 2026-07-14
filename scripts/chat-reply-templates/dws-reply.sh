#!/usr/bin/env bash
set -euo pipefail

command -v jq >/dev/null 2>&1 || {
  echo "dws-reply requires jq" >&2
  exit 1
}
command -v dws >/dev/null 2>&1 || {
  echo "dws-reply requires dws" >&2
  exit 1
}

payload=$(cat)
content=$(jq -er '.reply.content | select(type == "string" and length > 0)' <<<"$payload")
turn_id=$(jq -er '.id' <<<"$payload")
mode=$(jq -r '.reply_config.mode // "reply"' <<<"$payload")
ai_tag=$(jq -r '.reply_config.aiTag // true' <<<"$payload")

case "$mode" in
  reply)
    conversation_id=$(jq -er '.reply_config.openConversationId' <<<"$payload")
    message_id=$(jq -er '.reply_config.openMessageId' <<<"$payload")
    sender_id=$(jq -er '.reply_config.senderOpenDingTalkId' <<<"$payload")
    dws chat message reply \
      --conversation-id "$conversation_id" \
      --ref-msg-id "$message_id" \
      --ref-sender "$sender_id" \
      --text "$content" \
      --ai-tag="$ai_tag" \
      --uuid "$turn_id" \
      --format json
    ;;
  send)
    target_id=$(jq -er '.reply_config.openDingTalkId' <<<"$payload")
    dws chat message send \
      --open-dingtalk-id "$target_id" \
      --text "$content" \
      --ai-tag="$ai_tag" \
      --uuid "$turn_id" \
      --format json
    ;;
  *)
    echo "unsupported DWS reply mode: $mode" >&2
    exit 1
    ;;
esac
