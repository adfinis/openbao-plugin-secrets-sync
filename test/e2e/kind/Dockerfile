ARG OPENBAO_IMAGE=ghcr.io/openbao/openbao:2.6.4@sha256:cf2340fc9a22cb9358ca0defd1f39b65673836bd23fe2bb8984a07e11fe13ef4
FROM ${OPENBAO_IMAGE}

USER root
RUN mkdir -p /openbao/plugins
COPY bin/e2e/openbao-plugin-secrets-sync /openbao/plugins/openbao-plugin-secrets-sync
RUN chown -R 100:1000 /openbao/plugins && chmod 0755 /openbao/plugins/openbao-plugin-secrets-sync
USER 100:1000
