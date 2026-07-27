#!/usr/bin/env bash

# Keep these pins explicit so local protected-branch pushes and CI use the same
# scanner implementations. Update versions and checksums together after
# reviewing the upstream releases linked in scripts/docs/dependency-scanning.md.
GOVULNCHECK_VERSION="v1.6.0"
TRIVY_VERSION="0.72.0"

trivy_archive_checksum() {
  case "$1" in
    Linux-64bit)
      printf '%s\n' "bbb64b9695866ce4a7a8f5c9592002c5961cab378577fa3f8a040df362b9b2ea"
      ;;
    Linux-ARM64)
      printf '%s\n' "2ca2c023109c2db6b2b77366b6717291452d4531167377d95c79547f0c8e3467"
      ;;
    macOS-64bit)
      printf '%s\n' "ee5e60df8a98e5b89fd74a6d86f9e5c7e9a266a35002cb1e43291698b3bfee08"
      ;;
    macOS-ARM64)
      printf '%s\n' "88f208680dc05da2b459e19b4f5aa2b4dc7c2117892ba4aab2ae63baba330016"
      ;;
    *)
      return 1
      ;;
  esac
}
