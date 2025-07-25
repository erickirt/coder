#!/usr/bin/env bash

# This script generates release notes and publishes all of the given assets to
# GitHub releases. Depends on GitHub CLI.
#
# THIS IS NOT INTENDED TO BE CALLED BY DEVELOPERS! This is called by the release
# pipeline to do the final publish step. If you want to create a release use:
#   git tag -a -m "$ver" "$ver" && git push origin "$ver"
#
# Usage: ./publish.sh [--version 1.2.3] [--dry-run] path/to/asset1 path/to/asset2 ...
#
# The supplied images must already be pushed to the registry or this will fail.
# Also, the source images cannot be in a different registry than the target
# image generated by ./image_tag.sh.
# The supplied assets will be uploaded to the GitHub release as-is, as well as a
# file containing checksums.
#
# If no version is specified, defaults to the version from ./version.sh. The
# script will exit early if the branch is not tagged with the provided version
# (plus the "v" prefix) unless run with --dry-run.
#
# If the --dry-run parameter is supplied, the release will not be published to
# GitHub at all.
#
# Returns the link to the created GitHub release (unless --dry-run was
# specified).

set -euo pipefail
# shellcheck source=scripts/lib.sh
source "$(dirname "$(dirname "${BASH_SOURCE[0]}")")/lib.sh"

if [[ "${CI:-}" == "" ]]; then
	error "This script must be run in CI"
fi

stable=0
version=""
release_notes_file=""
dry_run=0

args="$(getopt -o "" -l stable,version:,release-notes-file:,dry-run -- "$@")"
eval set -- "$args"
while true; do
	case "$1" in
	--stable)
		stable=1
		shift
		;;
	--version)
		version="$2"
		shift 2
		;;
	--release-notes-file)
		release_notes_file="$2"
		shift 2
		;;
	--dry-run)
		dry_run=1
		shift
		;;
	--)
		shift
		break
		;;
	*)
		error "Unrecognized option: $1"
		;;
	esac
done

# Check dependencies
dependencies gh

# Remove the "v" prefix.
version="${version#v}"
if [[ "$version" == "" ]]; then
	version="$(execrelative ../version.sh)"
fi

if [[ -z $release_notes_file ]]; then
	error "No release notes files specified, use --release-notes-file."
fi

# realpath-ify all input files so we can cdroot below.
files=()
for f in "$@"; do
	if [[ ! -e "$f" ]]; then
		error "File not found: $f"
	fi
	files+=("$(realpath "$f")")
done
if [[ "${#files[@]}" == 0 ]]; then
	error "No files supplied"
fi

if [[ "$dry_run" == 0 ]] && [[ "$version" == *dev* ]]; then
	error "Cannot publish a dev version to GitHub"
fi

# The git commands need to be executed from within the repository.
cdroot

# Verify that we're currently checked out on the supplied tag.
new_tag="v$version"
if [[ "$(git describe --always)" != "$new_tag" ]]; then
	if [[ "$dry_run" == 0 ]]; then
		error "The provided version '$new_tag' does not match the current git describe output '$(git describe --always)'"
	fi

	log "The provided version does not match the current git tag, but --dry-run was supplied so continuing..."
fi

# Create temporary release folder so we can generate checksums. Both the
# sha256sum and gh binaries support symlinks as input files so this works well.
temp_dir="$(mktemp -d)"
for f in "${files[@]}"; do
	ln -s "$f" "$temp_dir/"
done

# Generate checksums file which will be uploaded to the GitHub release.
pushd "$temp_dir"
checksum_file="coder_${version}_checksums.txt"
sha256sum ./* | sed -e 's/\.\///' - >"$checksum_file"
popd

# Sign the checksums file if we have a GPG key. We skip this step in dry-run
# because we don't want to sign a fake release with our real key.
if [[ "$dry_run" == 0 ]] && [[ "${CODER_GPG_RELEASE_KEY_BASE64:-}" != "" ]]; then
	log "--- Signing checksums file"
	log

	execrelative ../sign_with_gpg.sh "${temp_dir}/${checksum_file}"
	signed_checksum_path="${temp_dir}/${checksum_file}.asc"

	if [[ ! -e "$signed_checksum_path" ]]; then
		log "Signed checksum file not found: ${signed_checksum_path}"
		log
		log "Files in ${temp_dir}:"
		ls -l "$temp_dir"
		log
		error "Failed to sign checksums file. See above for more details."
	fi

	log
	log
fi

log "--- Publishing release $new_tag on GitHub"
log
log "Description:"
sed -e 's/^/\t/' - <"$release_notes_file" 1>&2
log
log "Contents:"
pushd "$temp_dir"
find ./* 2>&1 | sed -e 's/^/\t/;s/\.\///' - 1>&2
popd
log
log

latest=false
if [[ "$stable" == 1 ]]; then
	latest=true
fi

target_commitish=main # This is the default.
# Skip during dry-runs
if [[ "$dry_run" == 0 ]]; then
	release_branch_refname=$(git branch --remotes --contains "${new_tag}" --format '%(refname)' '*/release/*')
	if [[ -n "${release_branch_refname}" ]]; then
		# refs/remotes/origin/release/2.9 -> release/2.9
		target_commitish="release/${release_branch_refname#*release/}"
	fi
fi

# We pipe `true` into `gh` so that it never tries to be interactive.
true |
	maybedryrun "$dry_run" gh release create \
		--latest="$latest" \
		--title "$new_tag" \
		--target "$target_commitish" \
		--notes-file "$release_notes_file" \
		"$new_tag" \
		"$temp_dir"/*

rm -rf "$temp_dir"
rm -rf "$release_notes_file"
