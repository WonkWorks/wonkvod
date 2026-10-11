#!/bin/sh
set -eu

deploy_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_dir=$(CDPATH= cd -- "$deploy_dir/.." && pwd)
compose_file="$deploy_dir/compose.yml"
action=${1:-deploy}

full_commit=$(git -C "$repo_dir" rev-parse HEAD)
short_commit=$(git -C "$repo_dir" rev-parse --short=7 HEAD)
export WONKVOD_VERSION="$short_commit"
export WONKVOD_GIT_COMMIT="$full_commit"

compose() {
	docker compose --project-name wonkvod --project-directory "$deploy_dir" -f "$compose_file" "$@"
}

require_clean_checkout() {
	if [ -n "$(git -C "$repo_dir" status --porcelain)" ]; then
		echo "refusing to build a dirty checkout; commit or stash changes first" >&2
		exit 1
	fi
}

verify_image() {
	image="wonkvod-backend:$short_commit"
	revision=$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' "$image")
	if [ "$revision" != "$full_commit" ]; then
		echo "image revision mismatch: expected $full_commit, found $revision" >&2
		exit 1
	fi
}

wait_for_backend() {
	count=0
	# A valid persisted catalog makes health available before the asynchronous
	# provider refresh completes. Cold bootstrap remains unhealthy until its
	# first usable snapshot is promoted.
	while [ "$count" -lt 90 ]; do
		state=$(docker inspect --format '{{.State.Status}}' wonkvod-backend 2>/dev/null || true)
		health=$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{end}}' wonkvod-backend 2>/dev/null || true)
		if [ "$state" = "running" ] && [ "$health" = "healthy" ]; then
			return 0
		fi
		if [ "$state" = "exited" ] || [ "$state" = "dead" ]; then
			echo "wonkvod-backend entered state $state" >&2
			exit 1
		fi
		count=$((count + 1))
		sleep 2
	done
	echo "timed out waiting for wonkvod-backend health" >&2
	exit 1
}

print_status() {
	container_image=$(docker inspect --format '{{.Config.Image}}' wonkvod-backend)
	container_image_id=$(docker inspect --format '{{.Image}}' wonkvod-backend)
	container_revision=$(docker inspect --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' wonkvod-backend)
	health=$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{end}}' wonkvod-backend)
	printf 'git_commit=%s\nimage=%s\nimage_id=%s\nimage_revision=%s\nhealth=%s\n' \
		"$full_commit" "$container_image" "$container_image_id" "$container_revision" "$health"
}

case "$action" in
	config)
		compose config --quiet
		;;
	build)
		require_clean_checkout
		compose config --quiet
		compose build wonkvod-backend
		verify_image
		printf 'built wonkvod-backend:%s from %s\n' "$short_commit" "$full_commit"
		;;
	deploy)
		require_clean_checkout
		compose config --quiet
		compose build wonkvod-backend
		verify_image
		compose up -d --no-deps wonkvod-backend
		wait_for_backend
		print_status
		;;
	status)
		print_status
		;;
	*)
		echo "usage: $0 [config|build|deploy|status]" >&2
		exit 2
		;;
esac
