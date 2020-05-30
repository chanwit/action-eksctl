FROM python:3.6-alpine

LABEL "com.github.actions.name"="action-eksctl"
LABEL "com.github.actions.description"="EKSctl and GitOps-based provisioner"
LABEL "com.github.actions.icon"="server"
LABEL "com.github.actions.color"="blue"

LABEL "repository"="https://github.com/chanwit/action-eksctl"
LABEL "homepage"="https://github.com/chanwit/action-eksctl"
LABEL "maintainer"="Chanwit Kaewkasi <chanwit@weave.works>"

ENV EKSCTL_VERSION  "0.20.0"
ENV YQ_VERSION      "3.3.0"
ENV AWS_IAM_VERSION "1.13.7/2019-06-11"
ENV KUBECTL_VERSION "v1.16.10"
ENV FLUXCTL_VERSION "1.19.0"

RUN apk add --update --no-cache curl openssl git gcc musl-dev libffi-dev libressl-dev \
    && curl -s -o /usr/local/bin/aws-iam-authenticator https://amazon-eks.s3-us-west-2.amazonaws.com/${AWS_IAM_VERSION}/bin/linux/amd64/aws-iam-authenticator \
    && chmod +x /usr/local/bin/aws-iam-authenticator \
    && curl -s -o /tmp/aws-iam-authenticator.sha256 https://amazon-eks.s3-us-west-2.amazonaws.com/${AWS_IAM_VERSION}/bin/linux/amd64/aws-iam-authenticator.sha256 \
    && openssl sha1 -sha256 /usr/local/bin/aws-iam-authenticator \
    && curl -s --location "https://github.com/weaveworks/eksctl/releases/download/${EKSCTL_VERSION}/eksctl_$(uname -s)_amd64.tar.gz" | tar xz -C /tmp \
    && mv /tmp/eksctl /usr/local/bin \
    && curl -s --location "https://github.com/mikefarah/yq/releases/download/${YQ_VERSION}/yq_linux_amd64" > /usr/local/bin/yq \
    && chmod +x /usr/local/bin/yq

RUN pip3 install awscli --upgrade

RUN curl -s --location "https://storage.googleapis.com/kubernetes-release/release/${KUBECTL_VERSION}/bin/linux/amd64/kubectl" > /usr/local/bin/kubectl \
    && chmod +x /usr/local/bin/kubectl

RUN curl -s --location "https://github.com/fluxcd/flux/releases/download/${FLUXCTL_VERSION}/fluxctl_linux_amd64" > /usr/local/bin/fluxctl \
    && chmod +x /usr/local/bin/fluxctl

RUN apk add --update --no-cache git openssh

COPY provisioner /usr/local/bin/provisioner

COPY entrypoint.sh /entrypoint.sh
RUN  chmod +x /entrypoint.sh

ENTRYPOINT /entrypoint.sh
