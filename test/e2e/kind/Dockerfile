ARG OPENBAO_IMAGE=ghcr.io/openbao/openbao:2.6.0@sha256:900bb64d0671cd1d82b693c56206f7263b582445f3a3bb6ba6e5213f524a6653
FROM ${OPENBAO_IMAGE}

USER root
RUN mkdir -p /openbao/plugins
COPY bin/e2e/openbao-plugin-secrets-sync /openbao/plugins/openbao-plugin-secrets-sync
RUN chown -R 100:1000 /openbao/plugins && chmod 0755 /openbao/plugins/openbao-plugin-secrets-sync
USER 100:1000
