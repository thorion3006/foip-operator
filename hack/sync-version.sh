#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
VERSION_FILE="$ROOT/VERSION"
CURRENT_VERSION=""
if [[ -f "$VERSION_FILE" ]]; then
  CURRENT_VERSION=$(tr -d '[:space:]' < "$VERSION_FILE")
fi

NEW_VERSION="${1:-$CURRENT_VERSION}"
NEW_VERSION="${NEW_VERSION#v}"

if [[ -z "$NEW_VERSION" ]]; then
  echo "usage: $0 <version>" >&2
  exit 1
fi

PREVIOUS_TAG=$(git -C "$ROOT" tag --list 'v*' --sort=-v:refname | awk -v current="v$NEW_VERSION" '$0 != current { print; exit }')
if [[ -z "$PREVIOUS_TAG" ]]; then
  PREVIOUS_TAG="v${CURRENT_VERSION:-$NEW_VERSION}"
fi

printf '%s\n' "$NEW_VERSION" > "$VERSION_FILE"

perl - "$ROOT" "$NEW_VERSION" "$CURRENT_VERSION" "$PREVIOUS_TAG" <<'PERL'
use strict;
use warnings;

my ($root, $new, $old, $previous) = @ARGV;
$old = defined($old) && length($old) ? $old : $new;

sub write_if_changed {
  my ($path, $transform) = @_;
  open my $in, '<', $path or die "open $path: $!";
  local $/;
  my $text = <$in>;
  close $in;
  my $updated = $transform->($text);
  if ($updated ne $text) {
    open my $out, '>', $path or die "write $path: $!";
    print {$out} $updated;
    close $out;
  }
}

write_if_changed("$root/charts/foip-operator/Chart.yaml", sub {
  my ($text) = @_;
  $text =~ s/^version:\s*.+$/version: $new/m;
  $text =~ s/^appVersion:\s*.+$/appVersion: $new/m;
  return $text;
});

for my $rel ('examples/helm/values-safe.yaml', 'examples/helm/values-node-health-only.yaml') {
  write_if_changed("$root/$rel", sub {
    my ($text) = @_;
    $text =~ s/^(\s+tag:\s*")[^"]+(")$/$1$new$2/m;
    return $text;
  });
}

write_if_changed("$root/README.md", sub {
  my ($text) = @_;
  $text =~ s{For the destructive v\Q$old\E migration procedure, see \[MIGRATION\.md\]\(MIGRATION\.md\)\.}{For the destructive v$new migration procedure, see [MIGRATION.md](MIGRATION.md).};
  $text =~ s{https://github\.com/thorion3006/foip-operator/compare/v[0-9.]+\.{3}v[0-9.]+}{https://github.com/thorion3006/foip-operator/compare/$previous...v$new};
  $text =~ s{service\.version=\Q$old\E,foip\.component=foip}{service.version=$new,foip.component=foip};
  return $text;
});
PERL
