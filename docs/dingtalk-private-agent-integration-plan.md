# DingTalk Private Agent Integration Plan

Goal: keep DingTalk Open Platform credentials only in `dingtalk-native-agent`, while Multica calls narrowly-scoped internal APIs over Railway private networking.

Architecture:
- `dingtalk-native-agent` exposes internal HTTP capabilities for organization user search and group-member addition.
- `multica-backend` keeps the existing members-tab public API, but its DingTalk transport becomes an internal-agent client when `DINGTALK_AGENT_BASE_URL` is set.
- The two services live in the same Railway project/environment and communicate via `http://dingtalk-native-agent.railway.internal:<port>`, protected by a shared internal API secret.

Tasks:
- Add `/internal/dingtalk/users/search` and `/internal/dingtalk/group-members` to `dingtalk-native-agent`.
- Add `DINGTALK_AGENT_BASE_URL` and `DINGTALK_AGENT_INTERNAL_SECRET` support to Multica backend.
- Deploy `dingtalk-native-agent` as a service inside the `FDE-Agent` Railway project and configure Multica backend to call it through the private domain.
- Verify the production members picker no longer returns `DingTalk integration is not configured`.

Plan: none
