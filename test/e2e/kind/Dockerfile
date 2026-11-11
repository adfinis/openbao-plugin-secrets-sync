ARG OPENBAO_IMAGE=ghcr.io/openbao/openbao:2.7.1@sha256:6d2b93856e3fcf7b18ad855a0b51eaba474dc8b79cf554379ea32034797d2acf
FROM ${OPENBAO_IMAGE}

USER root
RUN mkdir -p /openbao/plugins
COPY bin/e2e/openbao-plugin-secrets-sync /openbao/plugins/openbao-plugin-secrets-sync
RUN chown -R 100:1000 /openbao/plugins && chmod 0755 /openbao/plugins/openbao-plugin-secrets-sync
USER 100:1000
