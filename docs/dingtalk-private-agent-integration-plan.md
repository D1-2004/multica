# DingTalk Private Agent Integration Plan

Goal: keep DingTalk Open Platform credentials only in `dingtalk-native-agent`, while Multica calls narrowly-scoped internal APIs over Railway private networking.

Architecture:
- `dingtalk-native-agent` exposes internal HTTP capabilities for DingTalk OAuth user resolution, organization user search, and group-member addition.
- `multica-backend` keeps the existing login and members-tab public APIs, but its DingTalk transport becomes an internal-agent client when `DINGTALK_AGENT_BASE_URL` is set.
- The two services live in the same Railway project/environment and communicate via `http://dingtalk-native-agent.railway.internal:<port>`, protected by a shared internal API secret.

Tasks:
- Add `/internal/dingtalk/oauth/user`, `/internal/dingtalk/users/search`, and `/internal/dingtalk/group-members` to `dingtalk-native-agent`.
- Add `DINGTALK_AGENT_BASE_URL` and `DINGTALK_AGENT_INTERNAL_SECRET` support to Multica backend.
- Route DingTalk login identity resolution through the private agent so backend no longer needs DingTalk AppSecret in hosted deployments.
- Deploy `dingtalk-native-agent` as a service inside the `FDE-Agent` Railway project and configure Multica backend to call it through the private domain.
- Verify production login/config and the members picker no longer require DingTalk secrets in `multica-backend`.

Result:
- Implemented and deployed the private agent capabilities in the FDE-Agent Railway project.
- Multica backend now logs `dingtalk integration enabled via private agent` and no longer stores `DINGTALK_CLIENT_SECRET` in production variables.
- All three production services (`dingtalk-native-agent`, `multica-backend`, `multica-web`) were verified healthy after deployment.

Remaining:
- No code follow-up required for this integration. A real DingTalk authCode can only be exercised through the browser OAuth flow.

Plan: none
