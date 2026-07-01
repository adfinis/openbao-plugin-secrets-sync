ARG OPENBAO_IMAGE=ghcr.io/openbao/openbao:2.5.5@sha256:6150c4a6b62067db6141c8da7a6a6b5763f4f47c315343d0c848b40fecdfd452
FROM ${OPENBAO_IMAGE}

USER root
RUN mkdir -p /openbao/plugins
COPY bin/e2e/openbao-plugin-secrets-sync /openbao/plugins/openbao-plugin-secrets-sync
RUN chown -R 100:100 /openbao/plugins && chmod 0755 /openbao/plugins/openbao-plugin-secrets-sync
USER 100
