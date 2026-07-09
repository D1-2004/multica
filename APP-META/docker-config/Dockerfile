ARG APP_NAME=dt-fde-multica
ARG GO_BASE_IMAGE=hub.docker.alibaba-inc.com/aone-base-global/alios8-nodejs:2.0
ARG NODE_BASE_IMAGE=hub.docker.alibaba-inc.com/aone-base-global/alios8-nodejs:2.0

FROM ${GO_BASE_IMAGE} AS backend-builder
ARG APP_NAME
ARG GO_VERSION=1.26.1
WORKDIR /workspace
RUN curl -fsSLo /tmp/go.tgz "https://dl.google.com/go/go${GO_VERSION}.linux-amd64.tar.gz" && \
    rm -rf /usr/local/go && \
    tar -C /usr/local -xzf /tmp/go.tgz && \
    rm -f /tmp/go.tgz
ENV PATH="/usr/local/go/bin:${PATH}"
ENV GOPROXY=https://goproxy.cn
ENV GOSUMDB=sum.golang.google.cn
COPY ${APP_NAME}.tgz /tmp/source.tgz
RUN tar -xzf /tmp/source.tgz --strip-components=1 -C /workspace
RUN cd server && go mod download
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown
RUN cd server && CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" -o bin/server ./cmd/server
RUN cd server && CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" -o bin/multica ./cmd/multica
RUN cd server && CGO_ENABLED=0 go build -ldflags "-s -w" -o bin/migrate ./cmd/migrate
RUN cd server && CGO_ENABLED=0 go build -ldflags "-s -w" -o bin/backfill_task_usage_hourly ./cmd/backfill_task_usage_hourly
RUN cd server && CGO_ENABLED=0 go build -ldflags "-s -w" -o bin/backfill_codex_usage_cache ./cmd/backfill_codex_usage_cache

FROM ${NODE_BASE_IMAGE} AS web-builder
ARG APP_NAME
WORKDIR /workspace
ENV NPM_CONFIG_REGISTRY=https://registry.npmmirror.com
ENV npm_config_registry=https://registry.npmmirror.com
ENV COREPACK_NPM_REGISTRY=https://registry.npmmirror.com
ENV ELECTRON_SKIP_BINARY_DOWNLOAD=1
COPY ${APP_NAME}.tgz /tmp/source.tgz
RUN tar -xzf /tmp/source.tgz --strip-components=1 -C /workspace
RUN corepack enable && corepack prepare pnpm@10.28.2 --activate && pnpm config set registry "${NPM_CONFIG_REGISTRY}"
ENV REMOTE_API_URL=http://127.0.0.1:8080
ENV NEXT_PUBLIC_APP_VERSION=dev
ENV STANDALONE=true
RUN pnpm install --frozen-lockfile
RUN pnpm --filter @multica/web build

FROM ${NODE_BASE_IMAGE} AS node-runtime

FROM hub.docker.alibaba-inc.com/aone-base-global/alios8-nodejs:2.0
ARG APP_NAME=dt-fde-multica
ARG ENV_TYPE
ARG DWS_VERSION=v1.0.50
ARG DWS_RELEASE_BASE=https://github.com/DingTalk-Real-AI/dingtalk-workspace-cli/releases/download

EXPOSE 80
EXPOSE 3000
EXPOSE 8080

ENV APP_NAME=${APP_NAME}
ENV APP_HOME=/home/admin/${APP_NAME}
ENV SCRIPT_ENTRY=nodejsctl
ENV AONE_ENV_TYPE=${ENV_TYPE}
ENV NODE_ENV=production
ENV PORT=8080
ENV BACKEND_PORT=8080
ENV FRONTEND_PORT=3000
ENV HOSTNAME=0.0.0.0
ENV MULTICA_DWS_CLI_PATH=/usr/local/bin/dws
ENV PATH="/home/admin/${APP_NAME}/node/bin:/home/admin/${APP_NAME}/target/${APP_NAME}/bin:${PATH}"
ENV NPM_CONFIG_REGISTRY=https://registry.npmmirror.com
ENV npm_config_registry=https://registry.npmmirror.com

RUN npm install -g @e2b/cli@2.13.0 && npm cache clean --force
RUN --mount=type=secret,id=ghproxy \
    set -eux; \
    ghproxy="$(cat /run/secrets/ghproxy)"; \
    test -n "${ghproxy}"; \
    case "${ghproxy}" in */) ghproxy_prefix="${ghproxy}" ;; *) ghproxy_prefix="${ghproxy}/" ;; esac; \
    mkdir -p /tmp/dws; \
    base="${ghproxy_prefix}${DWS_RELEASE_BASE}/${DWS_VERSION}"; \
    curl -fsSLo /tmp/dws/checksums.txt "${base}/checksums.txt"; \
    curl -fsSLo /tmp/dws/dws-linux-amd64.tar.gz "${base}/dws-linux-amd64.tar.gz"; \
    cd /tmp/dws; \
    grep '  dws-linux-amd64.tar.gz$' checksums.txt | sha256sum -c -; \
    tar -xzf dws-linux-amd64.tar.gz; \
    install -m 0755 dws /usr/local/bin/dws; \
    rm -rf /tmp/dws; \
    dws version

RUN mkdir -p /home/admin/${APP_NAME}/target/${APP_NAME} /home/admin/${APP_NAME}/logs /home/admin/${APP_NAME}/node/bin /home/admin/cai/conf/apps && \
    cp -rf /home/admin/appname/bin /home/admin/${APP_NAME} && \
    mv /home/admin/${APP_NAME}/bin/start.sh /home/admin/start.sh

WORKDIR /home/admin/${APP_NAME}/bin

VOLUME /home/admin/logs /home/admin/cai/logs /home/admin/${APP_NAME}/logs /home/admin/${APP_NAME}/target/${APP_NAME}/data /home/a/vipsrv-dns/vipsrv-cache

COPY environment/cai/conf/nginx-proxy.conf /home/admin/cai/conf/apps/${APP_NAME}.conf
COPY environment/common/custom/app/bin/*.sh /home/admin/${APP_NAME}/bin/
COPY --from=node-runtime /usr/local/bin/node /home/admin/${APP_NAME}/node/bin/node
COPY --from=backend-builder /workspace/src/ /home/admin/${APP_NAME}/target/${APP_NAME}/src/
COPY --from=backend-builder /workspace/server/bin/ /home/admin/${APP_NAME}/target/${APP_NAME}/bin/
COPY --from=backend-builder /workspace/server/migrations/ /home/admin/${APP_NAME}/target/${APP_NAME}/migrations/
COPY --from=web-builder /workspace/apps/web/.next/standalone/ /home/admin/${APP_NAME}/target/${APP_NAME}/web/
COPY --from=web-builder /workspace/apps/web/.next/static/ /home/admin/${APP_NAME}/target/${APP_NAME}/web/apps/web/.next/static/
COPY --from=web-builder /workspace/apps/web/public/ /home/admin/${APP_NAME}/target/${APP_NAME}/web/apps/web/public/
COPY ${APP_NAME}.tgz /home/admin/${APP_NAME}/target/${APP_NAME}.tgz

RUN chmod +x /home/admin/start.sh /home/admin/${APP_NAME}/bin/*.sh /home/admin/${APP_NAME}/target/${APP_NAME}/bin/* /home/admin/${APP_NAME}/target/${APP_NAME}/src/main.sh /home/admin/${APP_NAME}/node/bin/node && \
    echo "/home/admin/${APP_NAME}/bin/nodejsctl pubstart" >> /home/admin/start.sh

ENTRYPOINT ["/home/admin/start.sh"]
