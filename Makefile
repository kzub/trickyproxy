.PHONY: bild publish

registry := 665720659514.dkr.ecr.us-east-1.amazonaws.com
service := "trickyproxy"
branch := $(shell git branch --show-current)
revision := $(shell git rev-parse HEAD)
image := ${registry}/${service}
tag := $(shell echo ${branch} | tr '[:upper:]' '[:lower:]' | tr -d "-")-${revision}

build:
	podman build -t ${image}:${tag} --build-arg GIT_BRANCH='${branch}' --build-arg REVISION='${revision}' --build-arg NPM_TOKEN='${npm_token}' .

publish:
	@aws --profile solar ecr get-login-password | podman login --username AWS --password-stdin ${registry}
	podman push ${image}:${tag}
