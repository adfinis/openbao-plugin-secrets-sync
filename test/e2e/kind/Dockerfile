ARG OPENBAO_IMAGE=ghcr.io/openbao/openbao:2.7.2@sha256:5046911075e137e154ce49ab051502f4ec7cf367d484ae4a4dce665d9b90561f
FROM ${OPENBAO_IMAGE}

USER root
RUN mkdir -p /openbao/plugins
COPY bin/e2e/openbao-plugin-secrets-sync /openbao/plugins/openbao-plugin-secrets-sync
RUN chown -R 100:1000 /openbao/plugins && chmod 0755 /openbao/plugins/openbao-plugin-secrets-sync
USER 100:1000
