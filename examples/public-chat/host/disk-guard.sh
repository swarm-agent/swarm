#!/bin/sh
# Keeps the bot host's disk from filling. Run hourly by disk-guard.timer.
# Build cache older than a week always goes. Above SOFT percent used, all
# build cache and untagged images go too (tagged rollback images are kept).
# Above HARD percent it logs at crit priority so the journal shows it.
set -eu
SOFT=${DISK_GUARD_SOFT:-70}
HARD=${DISK_GUARD_HARD:-85}
used() { df --output=pcent / | tail -1 | tr -dc 0-9; }

docker builder prune -f --filter until=168h >/dev/null
u=$(used)
if [ "$u" -ge "$SOFT" ]; then
  logger -t disk-guard -p user.warning "disk ${u}% used (soft limit ${SOFT}%): pruning build cache and untagged images"
  docker builder prune -af >/dev/null
  docker image prune -f >/dev/null
  u=$(used)
fi
if [ "$u" -ge "$HARD" ]; then
  logger -t disk-guard -p user.crit "DISK ${u}% used (hard limit ${HARD}%) after pruning; needs a person"
  exit 1
fi
logger -t disk-guard "disk ${u}% used"
